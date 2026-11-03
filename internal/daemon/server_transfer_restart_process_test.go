package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/transfer"
	"github.com/YoooClaw/cli/internal/transfercloud"
	"github.com/YoooClaw/cli/internal/transferjob"
)

type restartTransport struct {
	host string
	base http.RoundTripper
}

func (r restartTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.host
	c.URL = &u
	return r.base.RoundTrip(c)
}

// Opt-in: runs the production job/executor/importer in subprocesses, killing and
// recreating them against an isolated file-service/fulfillment fixture.
func TestCloudRestartProcess(t *testing.T) {
	if os.Getenv("YOOOCLAW_RESTART_PROCESS_TEST") != "1" {
		t.Skip("opt-in cloud process restart test")
	}
	pkg := t.TempDir()
	os.MkdirAll(filepath.Join(pkg, "blobs"), 0700)
	m := transfer.Manifest{SchemaVersion: 2, PackageID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	expected := map[string]string{}
	for i := 0; i < 200; i++ {
		body := make([]byte, 192*1024)
		rand.Read(body)
		h := fmt.Sprintf("%x", sha256.Sum256(body))
		name := fmt.Sprintf("file-%03d.bin", i)
		os.WriteFile(filepath.Join(pkg, "blobs", h), body, 0600)
		m.Files = append(m.Files, transfer.FullFile{Category: "workspace", Scope: "main", Path: name, Hash: h, Bytes: int64(len(body))})
		m.OriginalBytes += int64(len(body))
		expected[name] = h
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(pkg, "manifest.json"), raw, 0600)
	archive := filepath.Join(t.TempDir(), "package.tar.gz")
	if _, err := transfer.PackDirectory(pkg, archive); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"download", "validation", "import", "done"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			var resumed atomic.Bool
			var parts atomic.Int32
			var mu sync.Mutex
			var events []string
			var percents []int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "download-url") {
					json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]any{"taskId": strings.Repeat("a", 32), "signedUrl": "https://bucket.oss-cn-beijing.aliyuncs.com/backup?signature=test", "expiresAt": time.Now().Add(time.Hour).UnixMilli()}})
					return
				}
				if strings.HasSuffix(r.URL.Path, "events") {
					var e transferjob.Event
					json.NewDecoder(r.Body).Decode(&e)
					mu.Lock()
					events = append(events, e.EventType)
					mu.Unlock()
					json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "000000", "data": nil})
					return
				}
				if strings.HasSuffix(r.URL.Path, "download-progress") {
					var p struct {
						Progress int `json:"progress"`
					}
					json.NewDecoder(r.Body).Decode(&p)
					mu.Lock()
					percents = append(percents, p.Progress)
					mu.Unlock()
					json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "000000", "data": nil})
					return
				}
				var start, end int64
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
					w.WriteHeader(400)
					return
				}
				if phase == "download" && !resumed.Load() && start > 0 {
					<-r.Context().Done()
					return
				}
				if start == 0 && end > 0 {
					parts.Add(1)
				}
				w.Header().Set("ETag", `"stable"`)
				w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
				w.WriteHeader(206)
				w.Write(payload[start : end+1])
			}))
			defer srv.Close()
			start := func(first bool) (*exec.Cmd, *os.File) {
				log, err := os.Create(filepath.Join(root, fmt.Sprintf("worker-%t.log", first)))
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestCloudRestartWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "YC_RESTART_ROOT="+root, "YC_RESTART_URL="+srv.URL, "YC_RESTART_PHASE="+phase, fmt.Sprintf("YC_RESTART_FIRST=%t", first))
				cmd.Stdout = log
				cmd.Stderr = log
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				return cmd, log
			}
			cmd, log := start(true)
			deadline := time.Now().Add(45 * time.Second)
			ready := false
			for time.Now().Before(deadline) {
				if phase == "download" {
					if parts.Load() > 0 {
						time.Sleep(500 * time.Millisecond)
						ready = true
						break
					}
				}
				marker := filepath.Join(root, "pause")
				if phase == "import" {
					marker = filepath.Join(root, "home", "wuying-agent-workspace", "file-000.bin")
				}
				if _, err := os.Stat(marker); err == nil {
					ready = true
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			killedPID := cmd.Process.Pid
			cmd.Process.Kill()
			cmd.Wait()
			log.Close()
			if !ready {
				raw, _ := os.ReadFile(log.Name())
				t.Fatalf("phase never reached: %s", raw)
			}
			before, _ := os.ReadFile(filepath.Join(root, "jobs", "restart.json"))
			var old struct{ Deadline time.Time }
			json.Unmarshal(before, &old)
			resumed.Store(true)
			cmd, log = start(false)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					raw, _ := os.ReadFile(log.Name())
					t.Fatalf("restart failed: %s", raw)
				}
			case <-time.After(60 * time.Second):
				cmd.Process.Kill()
				t.Fatal("restart timeout")
			}
			log.Close()
			after, _ := os.ReadFile(filepath.Join(root, "jobs", "restart.json"))
			var current struct {
				Deadline time.Time
				Reported bool
			}
			json.Unmarshal(after, &current)
			if !current.Reported || !current.Deadline.Equal(old.Deadline) {
				t.Fatal("deadline changed or result missing")
			}
			for name, hash := range expected {
				body, err := os.ReadFile(filepath.Join(root, "home", "wuying-agent-workspace", name))
				if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != hash {
					t.Fatal("bad imported file", name, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(events, ",") != "IMPORT_STARTED,DOWNLOAD_COMPLETED,IMPORT_SUCCEEDED" {
				t.Fatalf("events=%v", events)
			}
			if len(percents) == 0 || percents[len(percents)-1] != 100 {
				t.Fatalf("missing completion progress: %v", percents)
			}
			if parts.Load() != 1 {
				t.Fatalf("first downloaded part fetched %d times", parts.Load())
			}
			t.Logf("phase=%s killedPID=%d recoveredPID=%d files=%d rawBytes=%d events=%v progress=%v deadlineUnchanged=true", phase, killedPID, cmd.Process.Pid, len(expected), m.OriginalBytes, events, percents)
		})
	}
}

func TestCloudRestartWorker(t *testing.T) {
	root := os.Getenv("YC_RESTART_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	url := os.Getenv("YC_RESTART_URL")
	phase := os.Getenv("YC_RESTART_PHASE")
	first := os.Getenv("YC_RESTART_FIRST") == "true"
	http.DefaultTransport = restartTransport{strings.TrimPrefix(url, "http://"), http.DefaultTransport}
	home := filepath.Join(root, "home")
	os.MkdirAll(home, 0700)
	importer := &transfer.Service{Home: home, Dir: filepath.Join(root, "transfers"), Roots: transfer.Roots{}}
	cloud := transfercloud.New("test-key", "api.example")
	base := cloud.ReportBaseURL("")
	complete := make(chan struct{}, 1)
	pause := func() { os.WriteFile(filepath.Join(root, "pause"), []byte("ready"), 0600); select {} }
	jobs, err := transferjob.New(filepath.Join(root, "jobs"), transferjob.Dependencies{
		Timeout: transfer.Timeout(), Identity: func(string) string { return "test-identity" }, Info: func(s string) { fmt.Println(s) }, Warn: func(s string) { fmt.Println(s) },
		Report: func(ctx context.Context, _ string, e transferjob.Event) error {
			err := cloud.ReportEvent(ctx, base, e)
			if err == nil && (e.EventType == "IMPORT_SUCCEEDED" || e.EventType == "IMPORT_FAILED") {
				complete <- struct{}{}
			}
			return err
		},
		Run: func(ctx context.Context, req transferjob.Request, _ string, emit func(string) error) error {
			err := executeTransferImport(ctx, req, transfercloud.ProgressOptions{TransferID: req.TransferID, BaseURL: base}, importDependencies{
				dir: filepath.Join(root, "jobs", req.TransferID), cleanup: importer.CleanupStaging, log: func(s string) { fmt.Println(s) }, download: cloud.DownloadWithProgress,
				handle: func(ctx context.Context, r transfer.Request) (any, error) {
					out, err := importer.HandleContext(ctx, r)
					if err == nil && r.Action == "preview" && first && phase == "validation" {
						pause()
					}
					return out, err
				},
			}, emit)
			if err == nil && first && phase == "done" {
				pause()
			}
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Stop()
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopped)
	if err = jobs.Accept(transferjob.Request{TransferID: "restart", FileTaskID: strings.Repeat("a", 32)}, "main"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
		fmt.Println("test worker received shutdown signal; preserving job")
		return
	case <-complete:
		time.Sleep(50 * time.Millisecond)
	case <-time.After(90 * time.Second):
		t.Fatal("worker timeout")
	}
}

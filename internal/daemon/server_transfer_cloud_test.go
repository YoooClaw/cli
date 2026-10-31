package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/transfer"
)

type acceptanceTransport func(*http.Request) (*http.Response, error)

func (f acceptanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Runs the real production binding, HTTP route, persistent worker, OSS range
// downloader and importer. Only remote fulfillment/file/OSS services are local
// HTTP fixtures; no production credentials or workspace are used.
func TestTransferCloudAcceptance(t *testing.T) {
	if os.Getenv("YOOOCLAW_TEST_CLOUD_ACCEPTANCE") != "1" {
		t.Skip("opt-in Wuying acceptance")
	}
	for _, scenario := range []string{"success", "corrupt", "download-failed", "start-denied", "result-retry", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			srv, ts := newTestServer(t, "acceptance-token")
			srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "target", Key: "acceptance-key"}}}
			srv.transfer = &transfer.Service{Dir: filepath.Join(t.TempDir(), "transfers"), Roots: transfer.Roots{transfer.TypeNotifications: srv.ctx.Paths.Notifications}, Notifications: srv.storage}
			host := config.ResolveCloudHost(srv.cfg)
			t.Setenv("YOOOCLAW_TRANSFER_REPORT_BASE_URL", "")
			t.Setenv("YOOOCLAW_TRANSFER_TIMEOUT", "45s")
			if scenario == "timeout" {
				t.Setenv("YOOOCLAW_TRANSFER_TIMEOUT", "150ms")
			}
			source := t.TempDir()
			raw := []byte(`{"clientLabel":"default","appName":"acceptance","title":"cloud-test","content":"isolated-cloud-data","timestamp":"2026-10-27T08:30:00+08:00"}` + "\n")
			if e := os.WriteFile(filepath.Join(source, "2026-10-27.jsonl"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			archive := filepath.Join(t.TempDir(), "package.tar.gz")
			if _, _, e := transfer.ExportArchive(transfer.Roots{transfer.TypeNotifications: source}, archive, transfer.ExportOptions{Include: "notifications"}); e != nil {
				t.Fatal(e)
			}
			payload, e := os.ReadFile(archive)
			if e != nil {
				t.Fatal(e)
			}
			if scenario == "corrupt" {
				payload = []byte("invalid archive")
			}
			const task = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			transferID := "cloud_" + strings.ReplaceAll(scenario, "-", "_")
			began := time.Now()
			var mu sync.Mutex
			var events []string
			var progress []int
			downloads, objects, terminalAttempts := 0, 0, 0
			terminal := make(chan string, 4)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				ok := func() { w.Write([]byte(`{"success":true,"code":"000000","data":null}`)) }
				if strings.HasPrefix(r.URL.Path, "/api/fulfillment/") || strings.HasPrefix(r.URL.Path, "/api/device/") {
					if r.Header.Get("X-Api-Key-Id") != "acceptance-key" {
						t.Error("wrong outbound credential")
						w.WriteHeader(403)
						return
					}
				}
				if r.URL.Path == "/api/fulfillment/plugin/transfer/events" {
					var b struct {
						TransferID   string `json:"transferId"`
						EventType    string `json:"eventType"`
						ErrorMessage string `json:"errorMessage"`
					}
					if json.NewDecoder(r.Body).Decode(&b) != nil || b.TransferID != transferID || r.Method != "POST" {
						t.Error("bad event contract")
						w.WriteHeader(400)
						return
					}
					mu.Lock()
					events = append(events, b.EventType)
					if b.EventType == "IMPORT_SUCCEEDED" || b.EventType == "IMPORT_FAILED" {
						terminalAttempts++
					}
					attempt := terminalAttempts
					mu.Unlock()
					t.Logf("HTTP POST events event=%s elapsed=%s", b.EventType, time.Since(began).Round(time.Millisecond))
					if b.EventType == "IMPORT_STARTED" && scenario == "start-denied" {
						w.WriteHeader(403)
						return
					}
					if b.EventType == "IMPORT_SUCCEEDED" && scenario == "result-retry" && attempt == 1 {
						w.WriteHeader(503)
						return
					}
					if b.EventType == "IMPORT_FAILED" && b.ErrorMessage == "" {
						t.Error("failure message missing")
					}
					ok()
					if b.EventType == "IMPORT_SUCCEEDED" || b.EventType == "IMPORT_FAILED" {
						terminal <- b.EventType
					}
					return
				}
				if r.URL.Path == "/api/fulfillment/plugin/transfer/"+transferID+"/download-progress" {
					var b struct {
						Progress int `json:"progress"`
					}
					if json.NewDecoder(r.Body).Decode(&b) != nil || r.Method != "PUT" {
						t.Error("bad progress contract")
						w.WriteHeader(400)
						return
					}
					mu.Lock()
					progress = append(progress, b.Progress)
					mu.Unlock()
					t.Logf("HTTP PUT download-progress=%d elapsed=%s", b.Progress, time.Since(began).Round(time.Millisecond))
					ok()
					return
				}
				if r.URL.Path == "/api/device/file/plugin/download-url" {
					var b map[string]string
					json.NewDecoder(r.Body).Decode(&b)
					if len(b) != 1 || b["taskId"] != task {
						t.Error("wrong file ID")
					}
					mu.Lock()
					downloads++
					mu.Unlock()
					if scenario == "download-failed" {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]string{"taskId": task, "signedUrl": "https://bucket.oss-cn-hangzhou.aliyuncs.com/package.tar.gz"}})
					return
				}
				if r.URL.Path == "/package.tar.gz" {
					if r.Header.Get("X-Api-Key-Id") != "" {
						t.Error("credential leaked to OSS")
					}
					mu.Lock()
					objects++
					mu.Unlock()
					if scenario == "timeout" {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(time.Second):
						}
					}
					var start, end int64
					if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= int64(len(payload)) {
						w.WriteHeader(400)
						return
					}
					w.Header().Set("ETag", `"fixed-acceptance"`)
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
					w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
					w.WriteHeader(206)
					w.Write(payload[start : end+1])
					return
				}
				t.Errorf("unexpected remote path %s", r.URL.Path)
				w.WriteHeader(404)
			}))
			defer remote.Close()
			transport := http.DefaultTransport
			http.DefaultTransport = acceptanceTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == host || r.URL.Host == "bucket.oss-cn-hangzhou.aliyuncs.com" {
					clone := r.Clone(r.Context())
					u := *r.URL
					u.Host = strings.TrimPrefix(remote.URL, "http://")
					u.Scheme = "http"
					clone.URL = &u
					return transport.RoundTrip(clone)
				}
				return transport.RoundTrip(r)
			})
			defer func() { http.DefaultTransport = transport }()
			if err := srv.startTransferImports(); err != nil {
				t.Fatal(err)
			}
			defer srv.transferImports.Stop()
			post := func(body string, authenticated bool) int {
				req, _ := http.NewRequest("POST", ts.URL+"/transfer/import/start", strings.NewReader(body))
				if authenticated {
					req.Header.Set("Authorization", "Bearer acceptance-token")
					req.Header.Set(relay.InternalHTTPHeader, "1")
					req.Header.Set(relay.InternalClientLabelHeader, "target")
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode == 200 {
					var b struct {
						Success bool   `json:"success"`
						Code    string `json:"code"`
						Data    any    `json:"data"`
					}
					if json.NewDecoder(resp.Body).Decode(&b) != nil || !b.Success || b.Code != "000000" || b.Data != nil {
						t.Error("bad ACK")
					}
				}
				return resp.StatusCode
			}
			body := fmt.Sprintf(`{"transferId":%q,"fileTaskId":%q}`, transferID, task)
			if got := post(body, false); got != 401 {
				t.Fatalf("unauthenticated=%d", got)
			}
			if got := post(`{"transferId":"bad","fileTaskId":"invalid"}`, true); got != 400 {
				t.Fatalf("invalid=%d", got)
			}
			acceptedAt := time.Now()
			if got := post(body, true); got != 200 {
				t.Fatalf("accept=%d", got)
			}
			t.Logf("POST /transfer/import/start ACK=200 latency=%s", time.Since(acceptedAt).Round(time.Millisecond))
			if got := post(body, true); got != 200 {
				t.Fatalf("duplicate=%d", got)
			}
			conflict := fmt.Sprintf(`{"transferId":%q,"fileTaskId":%q}`, transferID, strings.Repeat("b", 32))
			if got := post(conflict, true); got != 409 {
				t.Fatalf("conflict=%d", got)
			}
			want := "IMPORT_FAILED"
			if scenario == "success" || scenario == "result-retry" {
				want = "IMPORT_SUCCEEDED"
			}
			select {
			case got := <-terminal:
				if got != want {
					t.Fatalf("result=%s want=%s", got, want)
				}
			case <-time.After(40 * time.Second):
				t.Fatal("terminal event not received")
			}
			srv.transferImports.Cancel()
			// Wait for worker to finish before inspecting captures; Stop is safe to repeat.
			srv.transferImports.Stop()
			mu.Lock()
			defer mu.Unlock()
			if len(events) == 0 || events[0] != "IMPORT_STARTED" {
				t.Fatal(events)
			}
			if want == "IMPORT_SUCCEEDED" {
				if downloads != 1 {
					t.Fatalf("download repeated: %d", downloads)
				}
				if len(progress) < 2 || progress[0] != 0 || progress[len(progress)-1] != 100 {
					t.Fatal(progress)
				}
				if !strings.Contains(strings.Join(events, ","), "IMPORT_STARTED,DOWNLOAD_COMPLETED,IMPORT_SUCCEEDED") {
					t.Fatal(events)
				}
				files, _ := filepath.Glob(filepath.Join(srv.ctx.Paths.Notifications, "*.jsonl"))
				found := false
				for _, file := range files {
					data, _ := os.ReadFile(file)
					if strings.Contains(string(data), "isolated-cloud-data") {
						found = true
					}
				}
				if !found {
					t.Fatal("data did not reach target storage")
				}
			} else {
				for _, event := range events {
					if event == "DOWNLOAD_COMPLETED" || event == "IMPORT_SUCCEEDED" {
						t.Fatalf("invalid success progression: %v", events)
					}
				}
			}
			if scenario == "start-denied" && downloads != 0 {
				t.Fatal("download after rejected start")
			}
			if scenario == "download-failed" && downloads != 4 {
				t.Fatalf("download retry count=%d", downloads)
			}
			if scenario == "result-retry" && terminalAttempts != 2 {
				t.Fatalf("result attempts=%d", terminalAttempts)
			}
			t.Logf("EVIDENCE scenario=%s events=%v progress=%v downloadURLCalls=%d objectGETs=%d terminalAttempts=%d", scenario, events, progress, downloads, objects, terminalAttempts)
		})
	}
}

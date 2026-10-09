package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/testutil"
	"github.com/YoooClaw/cli/internal/transfer"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

func TestTransferCloudCommandRoundTrip(t *testing.T) {
	for _, previewOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(previewOnly), func(t *testing.T) {
			sandbox(t)
			const id = "0123456789abcdef0123456789abcdef"
			const key = "users/data-transfer/20260928/" + id + ".tar.gz"
			if _, err := creds.SetAPIKey("cli-test-key", false); err != nil {
				t.Fatal(err)
			}
			src := paths.For("source")
			testutil.WriteFile(t, filepath.Join(src.Notifications, "2026-09-28.jsonl"), []byte(`{"appName":"fixture","title":"migration","content":"synthetic test data","timestamp":"2026-09-28T08:00:00Z"}`+"\n"))
			dst := paths.For("default")
			logger := testutil.Logger{T: t}
			ns := notif.NewStorage(dst.Notifications, notif.PluginConfig{}, logger)
			if err := ns.Init(); err != nil {
				t.Fatal(err)
			}
			svc := &transfer.Service{Dir: filepath.Join(dst.Dir, "transfers"), Roots: transfer.Roots{transfer.TypeNotifications: dst.Notifications}, Notifications: ns}
			if err := daemon.WriteLock(dst, daemon.Lock{PID: os.Getpid(), Bind: "127.0.0.1", Port: 19000}); err != nil {
				t.Fatal(err)
			}
			var uploaded []byte
			deletes := 0
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				headers := make(http.Header)
				status := 200
				var data []byte
				respond := func(v any) { data, _ = json.Marshal(v) }
				cloudReply := func(v any) { respond(map[string]any{"code": "000000", "data": v}) }
				if strings.HasPrefix(r.URL.Path, "/api/device/file/plugin/") {
					if r.Header.Get("X-Api-Key-Id") != "cli-test-key" {
						t.Error("wrong API key")
					}
					for name := range r.Header {
						if strings.Contains(strings.ToLower(name), "scope") {
							t.Error("unexpected scope header")
						}
					}
				}
				switch r.URL.Path {
				case "/api/device/file/plugin/create":
					cloudReply(map[string]any{"taskId": id, "objectKey": key, "bucket": "bucket", "region": "cn-hangzhou", "endpoint": "https://oss-cn-hangzhou.aliyuncs.com", "credentials": map[string]string{"accessKeyId": "sts-id", "accessKeySecret": "sts-secret", "securityToken": "sts-token", "expiration": time.Now().Add(time.Hour).Format(time.RFC3339)}})
				case "/api/device/file/plugin/complete":
					cloudReply(map[string]any{"taskId": id, "objectKey": key, "status": "COMPLETED", "fileSize": strconv.Itoa(len(uploaded)), "signedUrl": "never-print-this"})
				case "/api/device/file/plugin/download-url":
					cloudReply(map[string]any{"taskId": id, "signedUrl": "https://bucket.oss-cn-hangzhou.aliyuncs.com/" + key + "?signature=never-print-this"})
				case "/api/device/file/plugin/delete":
					deletes++
					cloudReply(true)
				case "/transfer":
					var request transfer.Request
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					result, err := svc.Handle(request)
					if err != nil {
						t.Fatal(err)
					}
					respond(result)
				case "/" + key:
					if r.Header.Get("X-Api-Key-Id") != "" {
						t.Error("API key leaked to OSS")
					}
					if r.Method == "PUT" {
						uploaded, _ = io.ReadAll(r.Body)
						crc := oss.NewCRC64(0)
						crc.Write(uploaded)
						headers.Set("x-oss-hash-crc64ecma", fmt.Sprint(crc.Sum64()))
						headers.Set("ETag", `"fixture"`)
					} else if r.Method == "GET" {
						var start, end int
						if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
							t.Fatal(err)
						}
						data = uploaded[start : end+1]
						status = 206
						headers.Set("ETag", `"fixture"`)
						headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(uploaded)))
						headers.Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
					} else {
						t.Errorf("unexpected OSS method %s", r.Method)
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					status = 404
				}
				headers.Set("Content-Length", strconv.Itoa(len(data)))
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: r}, nil
			})
			out, code := execCLI(t, "--profile", "source", "transfer", "export", "--via", "oss", "--include", "notifications")
			if code != 0 {
				t.Fatalf("export: %s", out)
			}
			exported := decode(t, out)
			if exported["taskId"] != id || exported["records"] != float64(1) || exported["path"] != nil {
				t.Fatalf("unexpected export: %s", out)
			}
			for _, secret := range []string{"cli-test-key", "sts-secret", "never-print-this"} {
				if strings.Contains(out, secret) {
					t.Fatal("export leaked secrets")
				}
			}
			args := []string{"transfer", "import", "--task", id}
			if previewOnly {
				args = append(args, "--dry-run")
			}
			out, code = execCLI(t, args...)
			if code != 0 {
				t.Fatalf("import: %s", out)
			}
			result := decode(t, out)
			if previewOnly {
				if deletes != 0 || result["state"] != nil {
					t.Fatal("preview applied import or deleted cloud")
				}
				out, code = execCLI(t, "transfer", "import", "--local", result["localTransferId"].(string), "--plan", result["planId"].(string))
				if code != 0 {
					t.Fatalf("execute plan: %s", out)
				}
				result = decode(t, out)
			}
			if result["state"] != "SUCCEEDED" || deletes != 0 {
				t.Fatalf("result: %s deletes=%d", out, deletes)
			}
			if result["cloudCleanup"] != nil {
				t.Fatal("unexpected cloud cleanup result")
			}
		})
	}
}
func TestTransferRejectsAmbiguousCloudFlags(t *testing.T) {
	sandbox(t)
	cases := [][]string{
		{"export", "--via", "unknown"},
		{"export", "--via", "oss", "--out", "pkg.tar.gz"},
		{"import", "--task", "0123456789abcdef0123456789abcdef", "--file", "pkg.tar.gz"},
		{"import", "--task", "0123456789abcdef0123456789abcdef", "--resume"},
		{"import", "--task", "bad-id"},
	}
	for _, args := range cases {
		if out, code := execCLI(t, append([]string{"transfer"}, args...)...); code == 0 {
			t.Fatalf("accepted %v: %s", args, out)
		}
	}
}

func TestTransferHasNoCloudDeleteCommand(t *testing.T) {
	for _, command := range newTransferCmd().Commands() {
		if command.Name() == "delete" {
			t.Fatal("cloud deletion is owned by the server")
		}
	}
}

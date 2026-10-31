package daemon

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/transfer"
	"github.com/YoooClaw/cli/internal/transfercloud"
	"github.com/YoooClaw/cli/internal/transferjob"
)

func TestAutomaticTransferRouteUsesRelayAndRejectsArbitraryInput(t *testing.T) {
	srv, ts := newTestServer(t, "tok")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "target", Key: "key"}}}
	blocked := make(chan struct{})
	defer close(blocked)
	service, err := transferjob.New(t.TempDir(), transferjob.Dependencies{Timeout: time.Minute, Identity: func(string) string { return "identity" }, Run: func(ctx context.Context, _ transferjob.Request, _ string, _ func(string) error) error {
		select {
		case <-blocked:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, Report: func(context.Context, string, transferjob.Event) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	srv.transferImports = service
	post := func(body string, relayed bool) int {
		r, _ := http.NewRequest("POST", ts.URL+"/transfer/import/start", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer tok")
		if relayed {
			r.Header.Set(relay.InternalHTTPHeader, "1")
			r.Header.Set(relay.InternalClientLabelHeader, "target")
			r.Header.Set(relay.InternalRequestIDHeader, "relay_import_probe")
		}
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	body := `{"transferId":"one","fileTaskId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	if got := post(body, false); got != 403 {
		t.Fatal(got)
	}
	if got := post(`{"transferId":"one","fileTaskId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","file":"/tmp/arbitrary"}`, true); got != 400 {
		t.Fatal(got)
	}
	for i := 0; i < 2; i++ {
		if got := post(body, true); got != 200 {
			t.Fatal(got)
		}
	}
	raw, err := os.ReadFile(srv.logger.logFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`import.request_received requestId="relay_import_probe"`, `client="target"`, `transferId=one import.request_validated fileTaskId=`, `transferId=one import.response status=200 code=success requestId="relay_import_probe"`, `pid=`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, body) {
		t.Fatal("raw request body leaked")
	}
}
func TestImportExecutorOrdersVerificationAndPreservesPartialFailure(t *testing.T) {
	for _, state := range []string{"SUCCEEDED", "PARTIAL"} {
		t.Run(state, func(t *testing.T) {
			var steps []string
			var logs []string
			var archive string
			d := importDependencies{
				log: func(line string) { logs = append(logs, line) },
				download: func(_ context.Context, _ string, path string, _ transfercloud.ProgressOptions) (error, error) {
					archive = path
					steps = append(steps, "download")
					return os.WriteFile(path, []byte("archive"), 0600), nil
				},
				handle: func(_ context.Context, r transfer.Request) (any, error) {
					steps = append(steps, r.Action)
					if r.Action == "preview" {
						if _, err := os.Stat(archive); err != nil {
							t.Fatal("archive removed before validation", err)
						}
						return map[string]any{"localTransferId": "local", "planId": "plan"}, nil
					}
					if _, err := os.Stat(archive); !os.IsNotExist(err) {
						t.Fatal("archive must be removed before target writes")
					}
					return map[string]any{"state": state}, nil
				},
			}
			e := executeTransferImport(context.Background(), transferjob.Request{TransferID: "one", FileTaskID: strings.Repeat("a", 32)}, transfercloud.ProgressOptions{}, d, func(event string) error { steps = append(steps, event); return nil })
			if (e == nil) != (state == "SUCCEEDED") {
				t.Fatalf("state=%s error=%v", state, e)
			}
			if strings.Join(steps, ",") != "download,preview,DOWNLOAD_COMPLETED,import" {
				t.Fatal(steps)
			}
			if _, e = os.Stat(archive); !os.IsNotExist(e) {
				t.Fatal("temporary archive retained")
			}
			text := strings.Join(logs, "\n")
			for _, want := range []string{"transferId=one download.completed bytes=7 elapsedMs=", "transferId=one validation.completed elapsedMs=", "transferId=one import.writing"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %s: %s", want, text)
				}
			}
			if state == "SUCCEEDED" && !strings.Contains(text, "import.written records=0 workspaceItems=0 elapsedMs=") {
				t.Fatal(text)
			}
			if state == "PARTIAL" && !strings.Contains(text, "import.failed code=TRANSFER_IMPORT_PARTIAL") {
				t.Fatal(text)
			}
		})
	}
}

// Neither rejected previews nor failed state reports may proceed to import.
func TestPreviewOrEventRejectionStopsBeforeImport(t *testing.T) {
	for _, failAt := range []string{"preview", "event"} {
		t.Run(failAt, func(t *testing.T) {
			writes := 0
			d := importDependencies{
				download: func(context.Context, string, string, transfercloud.ProgressOptions) (error, error) { return nil, nil },
				handle: func(_ context.Context, r transfer.Request) (any, error) {
					if r.Action == "preview" {
						if failAt == "preview" {
							return nil, errors.New("invalid archive")
						}
						return map[string]any{"localTransferId": "local", "planId": "plan"}, nil
					}
					writes++
					return nil, nil
				},
			}
			e := executeTransferImport(context.Background(), transferjob.Request{}, transfercloud.ProgressOptions{}, d, func(string) error { return errors.New("rejected") })
			if e == nil || writes != 0 {
				t.Fatal("import continued after preview/event failure")
			}
		})
	}
}

func TestAutomaticImportRejectsCorruptArchiveUsingExistingImporter(t *testing.T) {
	svc := &transfer.Service{Dir: t.TempDir()}
	events := 0
	err := executeTransferImport(context.Background(), transferjob.Request{FileTaskID: strings.Repeat("a", 32)}, transfercloud.ProgressOptions{}, importDependencies{
		download: func(_ context.Context, _ string, path string, _ transfercloud.ProgressOptions) (error, error) {
			return os.WriteFile(path, []byte("not a tar.gz"), 0600), nil
		},
		handle: svc.HandleContext,
	}, func(string) error { events++; return nil })
	if err == nil || events != 0 {
		t.Fatal("corrupt archive passed validation")
	}
}

func TestAutomaticImportUsesRealArchiveAndImporterWithoutMetadata(t *testing.T) {
	srv, _ := newTestServer(t, "tok")
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "2026-10-27.jsonl"), []byte(`{"clientLabel":"default","appName":"test","title":"migration","content":"kept","timestamp":"2026-10-27T08:30:00+08:00"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, _, err := transfer.ExportArchive(transfer.Roots{transfer.TypeNotifications: source}, archive, transfer.ExportOptions{Include: "notifications"}); err != nil {
		t.Fatal(err)
	}
	svc := &transfer.Service{Dir: t.TempDir(), Roots: transfer.Roots{transfer.TypeNotifications: srv.ctx.Paths.Notifications}, Notifications: srv.storage}
	var events []string
	err := executeTransferImport(context.Background(), transferjob.Request{TransferID: "test", FileTaskID: strings.Repeat("a", 32)}, transfercloud.ProgressOptions{}, importDependencies{
		download: func(_ context.Context, _ string, dest string, _ transfercloud.ProgressOptions) (error, error) {
			raw, e := os.ReadFile(archive)
			if e != nil {
				return e, nil
			}
			return os.WriteFile(dest, raw, 0600), nil
		},
		handle: svc.HandleContext,
	}, func(e string) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != "DOWNLOAD_COMPLETED" {
		t.Fatal(events)
	}
	files, err := filepath.Glob(filepath.Join(srv.ctx.Paths.Notifications, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatal("notification not imported", err)
	}
	found := false
	for _, p := range files {
		raw, _ := os.ReadFile(p)
		if strings.Contains(string(raw), "kept") {
			found = true
		}
	}
	if !found {
		t.Fatal("imported notification missing")
	}
}

func TestTransferStageFailureLogsPreserveCodesWithoutProviderData(t *testing.T) {
	for _, stage := range []string{"download", "validation", "download_completion_report", "import"} {
		t.Run(stage, func(t *testing.T) {
			const secret = "https://private.example/?token=SECRET"
			var lines []string
			deps := importDependencies{
				log: func(line string) { lines = append(lines, line) },
				download: func(_ context.Context, _ string, path string, _ transfercloud.ProgressOptions) (error, error) {
					if stage == "download" {
						return errs.New("YOOOCLAW_TRANSFER_CLOUD_DOWNLOAD_FAILED", secret), nil
					}
					return os.WriteFile(path, []byte("archive"), 0600), nil
				},
				handle: func(_ context.Context, r transfer.Request) (any, error) {
					if stage == "validation" || (stage == "import" && r.Action == "import") {
						return nil, &transfer.Error{Code: "CHECKSUM_MISMATCH", Detail: secret}
					}
					return map[string]any{"localTransferId": "local", "planId": "plan"}, nil
				},
			}
			err := executeTransferImport(context.Background(), transferjob.Request{TransferID: "trace_job", FileTaskID: strings.Repeat("a", 32)}, transfercloud.ProgressOptions{}, deps, func(string) error {
				if stage == "download_completion_report" {
					return errors.New(secret)
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected failure")
			}
			text := strings.Join(lines, "\n")
			for _, want := range []string{"transferId=trace_job", stage + ".failed code=", "elapsedMs=", "totalElapsedMs="} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %s: %s", want, text)
				}
			}
			if stage == "download" && !strings.Contains(text, "code=YOOOCLAW_TRANSFER_CLOUD_DOWNLOAD_FAILED") {
				t.Fatal(text)
			}
			if stage == "validation" && !strings.Contains(text, "code=CHECKSUM_MISMATCH") {
				t.Fatal(text)
			}
			if strings.Contains(text, "SECRET") || strings.Contains(text, "private.example") {
				t.Fatal("provider data leaked")
			}
		})
	}
}

func TestTransferErrorLogDoesNotTreatBareUserTextAsCode(t *testing.T) {
	for _, message := range []string{"PRIVATE_USER_TEXT", "https://private.example/?token=SECRET"} {
		if got := transferLogErrorCode(errors.New(message)); got != "TRANSFER_STAGE_FAILED" {
			t.Fatalf("unexpected logged code: %s", got)
		}
	}
	if got := transferLogErrorCode(errors.New("TRANSFER_PLAN_INVALID")); got != "TRANSFER_PLAN_INVALID" {
		t.Fatal(got)
	}
}

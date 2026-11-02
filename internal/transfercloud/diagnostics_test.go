package transfercloud

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/transferjob"
)

func TestReportDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
		success          bool
	}{
		{"ok", `{"success":true,"code":"000000"}`, "000000", 200, true},
		{"http", `private response`, "http_error", 503, false},
		{"business", `{"success":false,"code":"403"}`, "403", 200, false},
		{"invalid", `private response`, "invalid_response", 200, false},
		{"private-code", `{"success":false,"code":"https://private/?token=secret"}`, "unknown", 200, false},
	} {
		for _, event := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "/event", false: "/progress"}[event], func(t *testing.T) {
				c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) })
				var got []ReportObservation
				ctx := WithReportLog(context.Background(), func(o ReportObservation) { got = append(got, o) })
				ctx = context.WithValue(ctx, reportAttemptKey{}, 2)
				var err error
				if event {
					err = c.ReportEvent(ctx, "https://api.example/fulfillment/transfer", transferjob.Event{TransferID: "tr_test", EventType: "IMPORT_STARTED"})
				} else {
					err = c.sendProgress(ctx, ProgressOptions{TransferID: "tr_test", BaseURL: "https://api.example/fulfillment/transfer"}, 42)
				}
				if len(got) != 1 {
					t.Fatalf("observations: %v", got)
				}
				o := got[0]
				if o.HTTPStatus != tc.status || o.Code != tc.code || o.Success != tc.success || (err == nil) != tc.success || o.Attempt != 2 || o.Elapsed <= 0 {
					t.Fatalf("unexpected: %+v err=%v", o, err)
				}
				if event && (o.Event != "IMPORT_STARTED" || o.Progress != -1) {
					t.Fatal(o)
				}
				if !event && (o.Event != "download_progress" || o.Progress != 42) {
					t.Fatal(o)
				}
			})
		}
	}
}
func TestDownloadMonitorIdleAndCleanup(t *testing.T) {
	type sample struct {
		written, total int64
		elapsed, idle  time.Duration
	}
	samples := make(chan sample, 100)
	m := newDownloadMonitor(func(w, total int64, e, i time.Duration) { samples <- sample{w, total, e, i} }, 5*time.Millisecond)
	next := func() sample {
		t.Helper()
		select {
		case s := <-samples:
			return s
		case <-time.After(time.Second):
			t.Fatal("missing idle heartbeat")
			return sample{}
		}
	}
	first := next()
	if first.written != 0 || first.total != -1 || first.idle <= 0 {
		t.Fatal(first)
	}
	m.update(20, 100)
	m.update(10, 100) // retries must not double count or regress bytes
	var last sample
	for {
		last = next()
		if last.written == 20 {
			break
		}
	}
	if last.total != 100 {
		t.Fatal(last)
	}
	later := next()
	if later.idle <= last.idle {
		t.Fatalf("idle not advancing: %v %v", last, later)
	}
	m.stop()
	count := len(samples)
	time.Sleep(15 * time.Millisecond)
	if len(samples) != count {
		t.Fatal("monitor logged after stop")
	}
}

func TestCanceledReportDiagnostics(t *testing.T) {
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request reached server") })
	var got ReportObservation
	ctx, cancel := context.WithCancel(WithReportLog(context.Background(), func(o ReportObservation) { got = o }))
	cancel()
	err := c.ReportEvent(ctx, "https://api.example/fulfillment/transfer", transferjob.Event{TransferID: "tr_test", EventType: "IMPORT_STARTED"})
	if err == nil || got.Success || got.HTTPStatus != 0 || got.Code != "canceled" {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
}

package transfercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestProgressContractAndConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status             int
		wantErr, permanent bool
	}{
		{"ok", `{"success":true,"code":"000000","data":null}`, 200, false, false},
		{"file-service-envelope", `{"code":"000000","data":null}`, 200, true, false},
		{"denied", `{"success":false,"code":"403"}`, 200, true, true},
		{"offline", `{}`, 503, true, false},
		{"redirect", `{}`, 302, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.URL.Path != "/fulfillment/transfer/tr_test/download-progress" {
					t.Errorf("unexpected route: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("X-Api-Key-Id") != "cli-key" {
					t.Error("missing credential")
				}
				var b map[string]int
				if json.NewDecoder(r.Body).Decode(&b) != nil || len(b) != 1 || b["progress"] != 65 {
					t.Errorf("wrong body: %v", b)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			o := ProgressOptions{TransferID: "tr_test", BaseURL: "https://api.example/fulfillment/transfer"}
			if err := c.ValidateProgress(o); err != nil {
				t.Fatal(err)
			}
			err := c.sendProgress(context.Background(), o, 65)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			var pe *progressError
			if tc.permanent && (!errors.As(err, &pe) || !pe.permanent) {
				t.Fatal("permanent error not recognized")
			}
			for _, bad := range []ProgressOptions{
				{TransferID: "../escape", BaseURL: o.BaseURL},
				{TransferID: o.TransferID, BaseURL: "https://other.example/transfer"},
				{TransferID: o.TransferID, BaseURL: "http://api.example/transfer"},
				{TransferID: o.TransferID, BaseURL: "https://api.example/transfer?token=x"},
				{TransferID: o.TransferID, BaseURL: o.BaseURL, Interval: time.Second},
			} {
				if c.ValidateProgress(bad) == nil {
					t.Fatalf("accepted bad options: %+v", bad)
				}
			}
		})
	}
}

func TestProgressCoalescesMonotonicAndFinishes(t *testing.T) {
	calls := make(chan int, 10)
	interval := 30 * time.Millisecond
	p := newProgressReporter(context.Background(), interval, func(_ context.Context, n int) error { calls <- n; return nil })
	if n := <-calls; n != 0 {
		t.Fatal(n)
	}
	p.update(10)
	p.update(35)
	p.update(20) // retry/out-of-order cannot decrease.
	select {
	case n := <-calls:
		t.Fatalf("not throttled: %d", n)
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case n := <-calls:
		if n != 35 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("no progress")
	}
	p.update(35)
	select {
	case n := <-calls:
		t.Fatalf("unchanged heartbeat: %d", n)
	case <-time.After(50 * time.Millisecond):
	}
	if err := p.finish(true); err != nil {
		t.Fatal(err)
	}
	if n := <-calls; n != 100 {
		t.Fatal(n)
	}
}

func TestProgressFailureKeepsLatestAndNeverBlocksCallback(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var values []int
	p := newProgressReporter(context.Background(), time.Millisecond, func(_ context.Context, n int) error {
		mu.Lock()
		values = append(values, n)
		count := len(values)
		mu.Unlock()
		if count == 1 {
			close(started)
			<-release
			return errors.New("temporary")
		}
		return nil
	})
	<-started
	done := make(chan struct{})
	go func() {
		for i := 1; i < 100; i++ {
			p.update(i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback blocked on HTTP")
	}
	close(release)
	if err := p.finish(true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if values[len(values)-1] != 100 || len(values) > 3 {
		t.Fatalf("queued stale progress: %v", values)
	}
}

func TestProgressPermanentFailureStopsReporting(t *testing.T) {
	calls := 0
	p := newProgressReporter(context.Background(), time.Millisecond, func(_ context.Context, n int) error { calls++; return &progressError{true} })
	p.update(90)
	if p.finish(true) == nil {
		t.Fatal("report failure hidden")
	}
	if calls != 1 {
		t.Fatalf("permanent error retried %d times", calls)
	}
}

func TestFailedDownloadNeverReports100(t *testing.T) {
	calls := make(chan int, 10)
	p := newProgressReporter(context.Background(), time.Millisecond, func(_ context.Context, n int) error { calls <- n; return nil })
	<-calls
	p.update(99)
	p.finish(false)
	close(calls)
	for n := range calls {
		if n == 100 {
			t.Fatal("failed download reported complete")
		}
	}
}

// Opt-in real SDK/HTTP probe for a slow cloud machine run. Normal unit tests
// cover the same coalescing state machine without a 22-second wall-clock wait.
func TestDownloadProgressSlowCloudProbe(t *testing.T) {
	if os.Getenv("YOOOCLAW_TEST_SLOW_PROGRESS") != "1" {
		t.Skip("set YOOOCLAW_TEST_SLOW_PROGRESS=1 for cloud acceptance")
	}
	payload := bytes.Repeat([]byte("progress-probe-"), 1300000)
	var mu sync.Mutex
	var values []int
	var times []time.Time
	startTime := time.Now()
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/download-url" {
			reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL})
			return
		}
		if r.URL.Path == "/fulfillment/transfer/tr_probe/download-progress" {
			var b struct {
				Progress int `json:"progress"`
			}
			if json.NewDecoder(r.Body).Decode(&b) != nil {
				t.Error("bad progress")
			}
			mu.Lock()
			values = append(values, b.Progress)
			times = append(times, time.Now())
			mu.Unlock()
			t.Logf("progress=%d elapsed=%s", b.Progress, time.Since(startTime).Round(time.Millisecond))
			w.Write([]byte(`{"success":true,"code":"000000","data":null}`))
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(400)
			return
		}
		if end > 0 {
			delay := time.Second
			if start >= partSize {
				delay = 18 * time.Second
			}
			if start >= 2*partSize {
				delay = 22 * time.Second
			}
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("ETag", `"fixed"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(206)
		w.Write(payload[start : end+1])
	})
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err, reportErr := c.DownloadWithProgress(ctx, testID, path, ProgressOptions{TransferID: "tr_probe", BaseURL: "https://api.example/fulfillment/transfer"})
	if err != nil || reportErr != nil {
		t.Fatalf("download=%v report=%v", err, reportErr)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("download differs")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(values) < 3 || values[0] != 0 || values[len(values)-1] != 100 {
		t.Fatalf("missing intermediate progress: %v", values)
	}
	for i := 1; i < len(values); i++ {
		if values[i] <= values[i-1] {
			t.Fatalf("nonmonotonic: %v", values)
		}
		if values[i] != 100 && times[i].Sub(times[i-1]) < 15*time.Second {
			t.Fatal("too frequent")
		}
	}
	t.Logf("verified %d bytes; progress=%v", len(got), values)
}

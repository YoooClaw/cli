package transfercloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/ossdownload"
)

func TestDownloadRenewalsDoNotExhaustRetryBudget(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Now().UnixMilli())
	var refreshes atomic.Int32
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath+"/download-url" {
			t.Error("unexpected route")
		}
		n := refreshes.Add(1)
		reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL + fmt.Sprintf("&generation=%d", n), "expiresAt": strconv.FormatInt(now.Load()+int64(30*time.Minute/time.Millisecond), 10)})
	})
	p := &renewingDownloadClient{cloud: c, taskID: testID, link: downloadLink{testID, signedURL, Number(now.Load() + int64(30*time.Minute/time.Millisecond))}, now: func() time.Time { return time.UnixMilli(now.Load()) }}
	if _, err := p.currentURL(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 0 {
		t.Fatal("refreshed outside window")
	}
	for cycle := 1; cycle <= 6; cycle++ {
		now.Add(int64(25 * time.Minute / time.Millisecond))
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := p.currentURL(context.Background()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if refreshes.Load() != int32(cycle) {
			t.Fatalf("cycle %d: %d refreshes", cycle, refreshes.Load())
		}
	}
}

func TestDownloadRenewalFailureIsSharedAndBounded(t *testing.T) {
	for _, kind := range []string{"http", "short", "missing"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if kind == "http" {
					w.WriteHeader(503)
					return
				}
				expiry := time.Now().Add(time.Minute).UnixMilli()
				if kind == "missing" {
					expiry = 0
				}
				reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL, "expiresAt": expiry})
			})
			p := &renewingDownloadClient{cloud: c, taskID: testID, link: downloadLink{testID, signedURL, Number(time.Now().UnixMilli())}, now: time.Now}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := p.currentURL(context.Background()); err == nil {
						t.Error("accepted invalid refresh")
					}
				}()
			}
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatalf("refresh storm: %d", calls.Load())
			}
		})
	}
}

// Exercise the actual SDK range downloader while the clock crosses the refresh
// window between requests. The byte stream and object identity remain unchanged.
func TestSDKDownloadRenewsURLWithoutRestarting(t *testing.T) {
	const size = int64(7)*partSize + 123
	var now atomic.Int64
	now.Store(time.Now().UnixMilli())
	var refreshes, heads, ranges atomic.Int32
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/download-url" {
			refreshes.Add(1)
			reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL + "&renewed=1", "expiresAt": now.Load() + int64(30*time.Minute/time.Millisecond)})
			return
		}
		if r.Header.Get("X-Api-Key-Id") != "" {
			t.Error("credential leaked")
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if end == 0 {
			heads.Add(1)
		} else {
			ranges.Add(1)
		}
		if heads.Load() > 1 {
			t.Error("download restarted")
		}
		w.Header().Set("ETag", `"same-object"`)
		w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(206)
		buf := make([]byte, 32*1024)
		for left := end - start + 1; left > 0; {
			n := min(int64(len(buf)), left)
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			left -= n
		}
		now.Add(int64(26 * time.Minute / time.Millisecond))
	})
	p := &renewingDownloadClient{cloud: c, taskID: testID, link: downloadLink{testID, signedURL, Number(now.Load() + int64(30*time.Minute/time.Millisecond))}, now: func() time.Time { return time.UnixMilli(now.Load()) }}
	dir := t.TempDir()
	dest := filepath.Join(dir, "package")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ossdownload.Download(ctx, p, signedURL, testID, dest, dir, MaxDownloadBytes, false); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, err := io.Copy(io.Discard, f)
	if err != nil || n != size || heads.Load() != 1 || ranges.Load() != 8 || refreshes.Load() == 0 {
		t.Fatalf("bytes=%d heads=%d ranges=%d refreshes=%d err=%v", n, heads.Load(), ranges.Load(), refreshes.Load(), err)
	}
}

func TestUnexpectedExpiryStillUsesFailureRetryBudget(t *testing.T) {
	var links, gets atomic.Int32
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/download-url" {
			links.Add(1)
			reply(w, map[string]any{"taskId": testID, "signedUrl": signedURL, "expiresAt": time.Now().Add(time.Hour).UnixMilli()})
			return
		}
		gets.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := c.Download(ctx, testID, filepath.Join(t.TempDir(), "package"))
	if err == nil || links.Load() != 4 || gets.Load() != 4 {
		t.Fatalf("links=%d gets=%d err=%v", links.Load(), gets.Load(), err)
	}
}

func TestDownloadRenewalHonorsCancellation(t *testing.T) {
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request after cancellation") })
	p := &renewingDownloadClient{cloud: c, taskID: testID, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.currentURL(ctx); err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}

package recording

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSDKDownloadResumesInterruptedRange(t *testing.T) {
	payload := bytes.Repeat([]byte("migration-data-"), 1300000)
	var mu sync.Mutex
	interrupted, resumed := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("signed GET URL used with another method")
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("X-Api-Key-Id") != "" {
			t.Error("API key forwarded to OSS")
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error("missing range")
			w.WriteHeader(400)
			return
		}
		if start > (8<<20) && start < 2*(8<<20) {
			mu.Lock()
			resumed = true
			mu.Unlock()
		}
		w.Header().Set("ETag", `"stable-version"`)
		w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(206)
		mu.Lock()
		breakNow := start == (8<<20) && !interrupted
		if breakNow {
			interrupted = true
		}
		mu.Unlock()
		if breakNow {
			w.Write(payload[start : start+300000])
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Write(payload[start : end+1])
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "package.tar.gz")
	result := DownloadFile(srv.URL, path, testLogger{t}, DownloadOptions{MaxRetries: 1, OverallTimeout: 15 * time.Second})
	if !result.OK {
		t.Fatal(result.Error)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("download bytes differ")
	}
	mu.Lock()
	defer mu.Unlock()
	if !interrupted || !resumed {
		t.Fatalf("interrupted=%v resumed=%v", interrupted, resumed)
	}
}

func TestDownloadRefreshOnce(t *testing.T) {
	for _, succeed := range []bool{true, false} {
		t.Run(fmt.Sprint(succeed), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if succeed && r.URL.Path == "/new" {
					w.Write([]byte("audio"))
					return
				}
				w.WriteHeader(403)
			}))
			defer srv.Close()
			dest := filepath.Join(t.TempDir(), "audio")
			result := DownloadFile(srv.URL+"/expired", dest, testLogger{t}, DownloadOptions{MaxRetries: 1, RefreshURL: func(context.Context) (string, error) { calls.Add(1); return srv.URL + "/new", nil }})
			if result.OK != succeed || calls.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
		})
	}
}
func TestDownloadIdleAndHeaderTimeout(t *testing.T) {
	for _, headers := range []bool{true, false} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.Header().Set("Content-Length", "10")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			start := time.Now()
			result := DownloadFile(srv.URL, filepath.Join(t.TempDir(), "audio"), testLogger{t}, DownloadOptions{MaxRetries: 1, HeaderTimeout: 30 * time.Millisecond, IdleTimeout: 30 * time.Millisecond, OverallTimeout: time.Second})
			if result.OK || time.Since(start) > 500*time.Millisecond {
				t.Fatalf("timeout failed: %+v, %s", result, time.Since(start))
			}
		})
	}
}
func TestDownloadRejectsRefreshedRedirect(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.Write([]byte("bad")) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/new" {
			http.Redirect(w, r, target.URL, 302)
			return
		}
		w.WriteHeader(403)
	}))
	defer srv.Close()
	result := DownloadFile(srv.URL, filepath.Join(t.TempDir(), "audio"), testLogger{t}, DownloadOptions{MaxRetries: 1, RefreshURL: func(context.Context) (string, error) { return srv.URL + "/new", nil }})
	if result.OK || leaked.Load() != 0 {
		t.Fatalf("redirect followed: %+v", result)
	}
}

func TestRefreshKeepsCheckpointAndRejectsChangedVersion(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			const part = 8 << 20
			payload := bytes.Repeat([]byte("a"), part*3+100)
			nextPayload := payload
			if changed {
				nextPayload = bytes.Repeat([]byte("b"), len(payload))
			}
			var newFirstPart atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var start, end int
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				fresh := r.URL.Path == "/new"
				if !fresh && start >= part*3 {
					w.WriteHeader(403)
					return
				}
				data := payload
				etag := `"original"`
				if fresh {
					data = nextPayload
					if changed {
						etag = `"changed"`
					}
					if start == 0 && end > 0 {
						newFirstPart.Add(1)
					}
				}
				w.Header().Set("ETag", etag)
				w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				w.WriteHeader(206)
				w.Write(data[start : end+1])
			}))
			defer srv.Close()
			dest := filepath.Join(t.TempDir(), "audio")
			result := DownloadFile(srv.URL+"/old", dest, testLogger{t}, DownloadOptions{MaxRetries: 1, OverallTimeout: 10 * time.Second, RefreshURL: func(context.Context) (string, error) { return srv.URL + "/new", nil }})
			if !result.OK {
				t.Fatal(result.Error)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, nextPayload) {
				t.Fatal("mixed or incomplete versions", err)
			}
			if (newFirstPart.Load() > 0) != changed {
				t.Fatalf("changed=%v first part redownloads=%d", changed, newFirstPart.Load())
			}
		})
	}
}

func TestDownloadBoundsZeroProgressRetries(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe := r.Header.Get("Range") == "bytes=0-0"
		end := 9
		if probe {
			end = 0
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/10", end))
		w.Header().Set("Content-Length", strconv.Itoa(end+1))
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(206)
		if probe {
			w.Write([]byte("a"))
			return
		}
		attempts.Add(1)
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()
	result := DownloadFile(srv.URL, filepath.Join(t.TempDir(), "audio"), testLogger{t}, DownloadOptions{MaxRetries: 1, OverallTimeout: time.Second})
	if result.OK || attempts.Load() != 3 {
		t.Fatalf("result=%+v attempts=%d", result, attempts.Load())
	}
}

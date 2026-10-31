package transfercloud

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Explicit opt-in: creates one synthetic file-service task. Uses no user data.
func TestLiveDownloadRenewal(t *testing.T) {
	credentialsPath := os.Getenv("YOOOCLAW_LIVE_RENEWAL_CREDENTIALS")
	if credentialsPath == "" {
		t.Skip("live file-service test is opt-in")
	}
	raw, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal("read credentials failed")
	}
	var credentials struct {
		APIKey string `json:"apiKey"`
	}
	if json.Unmarshal(raw, &credentials) != nil || credentials.APIKey == "" {
		t.Fatal("invalid credentials")
	}
	c := New(credentials.APIKey, "openclaw-service-test.yoooclaw.com")
	dir := t.TempDir()
	src := filepath.Join(dir, "renewal-probe.tar.gz")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	_, err = io.CopyN(gz, rand.Reader, 40<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	receipt, err := c.Upload(ctx, src)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	t.Logf("synthetic upload complete task=%s bytes=%d", receipt.TaskID, int64(receipt.FileSize))
	var mu sync.Mutex
	links, heads, parts := 0, 0, 0
	var firstURL, lastURL string
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && r.Header.Get("Range") != "bytes=0-0" {
			mu.Lock()
			parts++
			first := parts == 1
			mu.Unlock()
			if first {
				select {
				case <-time.After(2 * time.Second):
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
		}
		if r.Method == "GET" && r.Header.Get("Range") == "bytes=0-0" {
			mu.Lock()
			heads++
			mu.Unlock()
		}
		res, e := http.DefaultTransport.RoundTrip(r)
		if e != nil {
			return nil, e
		}
		if strings.HasSuffix(r.URL.Path, "/download-url") {
			body, e := io.ReadAll(res.Body)
			res.Body.Close()
			if e != nil {
				return nil, e
			}
			var envelope struct {
				Code string       `json:"code"`
				Data downloadLink `json:"data"`
			}
			if e = json.Unmarshal(body, &envelope); e != nil {
				return nil, e
			}
			mu.Lock()
			links++
			if links == 1 {
				firstURL = envelope.Data.SignedURL
				envelope.Data.ExpiresAt = Number(time.Now().Add(refreshWindow + time.Second).UnixMilli())
			}
			lastURL = envelope.Data.SignedURL
			mu.Unlock()
			body, e = json.Marshal(envelope)
			if e != nil {
				return nil, e
			}
			res.Body = io.NopCloser(bytes.NewReader(body))
			res.ContentLength = int64(len(body))
		}
		return res, nil
	})
	dest := filepath.Join(dir, "downloaded.tar.gz")
	started := time.Now()
	if err = c.Download(ctx, receipt.TaskID, dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	hash := func(path string) [32]byte {
		data, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return sha256.Sum256(data)
	}
	sourceHash, targetHash := hash(src), hash(dest)
	if sourceHash != targetHash {
		t.Fatal("hash mismatch")
	}
	mu.Lock()
	defer mu.Unlock()
	if links < 2 || heads != 1 || firstURL == lastURL {
		t.Fatalf("renewal missing or downloader restarted: links=%d heads=%d urlChanged=%t", links, heads, firstURL != lastURL)
	}
	t.Logf("PASS refreshed links=%d metadataProbes=%d partRequests=%d urlChanged=true sha256=%x elapsed=%s", links, heads, parts, targetHash, time.Since(started).Round(time.Millisecond))
}

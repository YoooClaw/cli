package recording

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func closedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestDownloadFileFallsBackFromUnavailableLoopbackProxy(t *testing.T) {
	t.Parallel()
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("audio-via-direct"))
	}))
	defer target.Close()

	proxyURL, err := url.Parse("http://" + closedLoopbackAddress(t))
	if err != nil {
		t.Fatal(err)
	}
	client := target.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client.Transport = transport

	dest := filepath.Join(t.TempDir(), "audio.ogg")
	result := DownloadFile(target.URL+"/audio.ogg", dest, testLogger{t}, DownloadOptions{
		Client:     client,
		MaxRetries: 1,
		Timeout:    2 * time.Second,
	})
	if !result.OK {
		t.Fatalf("download should fall back to direct connection: %+v", result)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "audio-via-direct" {
		t.Fatalf("unexpected audio: %q", data)
	}
}

func TestDownloadFailurePreservesExistingAudioAndDoesNotRetry404(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.NotFound(w, nil)
	}))
	defer target.Close()

	dest := filepath.Join(t.TempDir(), "audio.ogg")
	if err := os.WriteFile(dest, []byte("known-good-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := DownloadFile(target.URL+"/missing.ogg", dest, testLogger{t}, DownloadOptions{
		MaxRetries:   3,
		RetryBackoff: time.Millisecond,
	})
	if result.OK {
		t.Fatal("404 download should fail")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("404 should not be retried, requests=%d", got)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("existing audio was removed: %v", err)
	}
	if string(data) != "known-good-audio" {
		t.Fatalf("existing audio was modified: %q", data)
	}
	parts, err := filepath.Glob(filepath.Join(filepath.Dir(dest), ".audio-*.part"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Fatalf("temporary files leaked: %v", parts)
	}
}

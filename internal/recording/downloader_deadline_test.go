package recording

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeaderDeadlineCompletionSuppressesQueuedCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadline := &headerDeadline{cancel: cancel}
	if deadline.finish() {
		t.Fatal("unexpected timeout")
	}
	// A timer callback may already be queued when Timer.Stop returns false.
	deadline.expire()
	if ctx.Err() != nil {
		t.Fatal("completed response was cancelled")
	}
}
func TestHeaderDeadlineExpiryWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadline := &headerDeadline{cancel: cancel}
	deadline.expire()
	if !deadline.finish() || ctx.Err() == nil {
		t.Fatal("expired response accepted")
	}
}
func TestHeaderTimeoutStillCoversIncompleteHeaders(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1\r\n")
		buf.Flush()
		// Wait for the timed out client to disconnect, without finishing the headers.
		conn.SetReadDeadline(time.Now().Add(time.Second))
		one := make([]byte, 1)
		conn.Read(one)
	}))
	defer source.Close()
	client := &downloadClient{client: source.Client(), header: 30 * time.Millisecond}
	req, _ := http.NewRequest(http.MethodGet, source.URL, nil)
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected header timeout, got %v", err)
	}
}

type downloadLogCapture struct{ messages []string }

func (l *downloadLogCapture) Info(s string)  { l.messages = append(l.messages, s) }
func (l *downloadLogCapture) Warn(s string)  { l.messages = append(l.messages, s) }
func (l *downloadLogCapture) Error(s string) { l.messages = append(l.messages, s) }
func TestDownloadTerminalFailuresAreLoggedWithoutSignedURL(t *testing.T) {
	for _, code := range []int{404, 403} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer source.Close()
			log := &downloadLogCapture{}
			opts := DownloadOptions{MaxRetries: 1}
			if code == 403 {
				opts.RefreshURL = func(context.Context) (string, error) { return "", errors.New("refresh rejected") }
			}
			result := DownloadFile(source.URL+"?signature=secret-value", filepath.Join(t.TempDir(), "audio"), log, opts)
			messages := strings.Join(log.messages, "\n")
			if result.OK || !strings.Contains(messages, "开始下载") || !strings.Contains(messages, "下载失败") {
				t.Fatalf("missing lifecycle logs: %s", messages)
			}
			if code == 403 && !strings.Contains(messages, "签名链接刷新失败") {
				t.Fatal("missing refresh failure")
			}
			if strings.Contains(messages, source.URL) || strings.Contains(messages, "secret-value") {
				t.Fatal("signed URL leaked")
			}
		})
	}
}

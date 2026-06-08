package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestNewClientBuildsURL(t *testing.T) {
	p := sandboxPaths(t)
	WriteLock(p, Lock{PID: 1, Bind: "0.0.0.0", Port: 12345})
	c := NewClient(p)
	// 0.0.0.0 应改写成 127.0.0.1
	if !strings.Contains(c.BaseURL, "127.0.0.1:12345") {
		t.Errorf("base url = %q", c.BaseURL)
	}
}

func TestClientRequestDaemonNotRunning(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", timeout: 1e9}
	_, _, err := c.Request("GET", "/health", nil)
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeDaemonNotRunning {
		t.Errorf("connection refused should map to DAEMON_NOT_RUNNING, got %v", err)
	}
}

func TestClientRequestSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"echo":1}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, token: "tok", timeout: 5e9}
	status, body, err := c.Request("POST", "/x", map[string]any{"a": 1})
	if err != nil || status != 200 {
		t.Fatalf("request: status=%d err=%v", status, err)
	}
	m, ok := body.(map[string]any)
	if !ok || m["echo"] != float64(1) {
		t.Errorf("parsed body wrong: %+v", body)
	}
}

func TestClientRequestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, timeout: 5e9}
	status, _, err := c.Request("GET", "/x", nil)
	if status != 401 {
		t.Errorf("status = %d", status)
	}
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeUnauthorized {
		t.Errorf("401 should map to Unauthorized, got %v", err)
	}
}

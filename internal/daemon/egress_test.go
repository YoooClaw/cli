package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/config"
)

func TestResolveIngressMode(t *testing.T) {
	cfg := config.Default("/tmp/creds.json") // ingress.mode = standalone
	cases := []struct {
		name    string
		optMode string
		env     string
		cfgMode string
		want    string
	}{
		{"default standalone", "", "", config.IngressStandalone, config.IngressStandalone},
		{"flag wins over env+config", config.IngressDirect, config.IngressProxied, config.IngressProxied, config.IngressDirect},
		{"env wins over config", "", config.IngressProxied, config.IngressStandalone, config.IngressProxied},
		{"config used when no flag/env", "", "", config.IngressProxied, config.IngressProxied},
		{"unknown falls back to standalone", "bogus", "", config.IngressStandalone, config.IngressStandalone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("YOOOCLAW_INGRESS", tc.env)
			} else {
				t.Setenv("YOOOCLAW_INGRESS", "")
			}
			cfg.Ingress.Mode = tc.cfgMode
			got := resolveIngressMode(StartOpts{IngressMode: tc.optMode}, cfg)
			if got != tc.want {
				t.Fatalf("resolveIngressMode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveProxyEgress(t *testing.T) {
	logger := NewLogger(filepath.Join(t.TempDir(), "d.log"), "info", false)
	cfg := config.Default("/tmp/creds.json")

	// 未配置回调 → NoopEgress（出站丢弃）。
	if e := resolveProxyEgress(StartOpts{}, cfg, logger); e != (NoopEgress{}) {
		t.Fatalf("无回调时应为 NoopEgress，得到 %T", e)
	}

	// flag 提供回调 → ProxyEgress。
	e := resolveProxyEgress(StartOpts{EgressCallbackURL: "http://x/cb", EgressCallbackToken: "tok"}, cfg, logger)
	if _, ok := e.(*ProxyEgress); !ok {
		t.Fatalf("有回调时应为 *ProxyEgress，得到 %T", e)
	}
}

func TestProxyEgressPushEvent(t *testing.T) {
	type callback struct {
		auth string
		body string
	}
	got := make(chan callback, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- callback{auth: r.Header.Get("Authorization"), body: string(b)}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	logger := NewLogger(filepath.Join(t.TempDir(), "d.log"), "info", false)

	eg := NewProxyEgress(srv.URL, "cbtok", logger)
	if err := eg.PushEvent("recording.status", map[string]any{"id": "r1"}); err != nil {
		t.Fatalf("PushEvent error: %v", err)
	}
	var received callback
	select {
	case received = <-got:
	case <-time.After(time.Second):
		t.Fatal("未收到异步 egress 回调")
	}
	if received.auth != "Bearer cbtok" {
		t.Fatalf("Authorization = %q, want Bearer cbtok", received.auth)
	}
	var payload struct {
		Event   string         `json:"event"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(received.body), &payload); err != nil {
		t.Fatalf("回调 body 非法 JSON: %v (%s)", err, received.body)
	}
	if payload.Event != "recording.status" || payload.Payload["id"] != "r1" {
		t.Fatalf("回调 payload 不符: %+v", payload)
	}
}

func TestProxyEgressDoesNotWaitForSlowCallback(t *testing.T) {
	callbackStarted := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackStarted <- struct{}{}
		<-releaseCallback
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	eg := NewProxyEgress(srv.URL, "", nil)
	returned := make(chan error, 1)
	go func() {
		returned <- eg.PushEvent("recording.status", map[string]any{"id": "slow"})
	}()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("PushEvent error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(releaseCallback)
		t.Fatal("PushEvent 被慢宿主回调阻塞")
	}

	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		close(releaseCallback)
		t.Fatal("异步回调未开始")
	}
	close(releaseCallback)
}

func TestNoopEgressDropsEvents(t *testing.T) {
	if err := (NoopEgress{}).PushEvent("recording.status", nil); err != nil {
		t.Fatalf("NoopEgress 不应报错: %v", err)
	}
}

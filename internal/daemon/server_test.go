package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/paths"
)

// newTestServer 构造一个最小可用的 server（无 relay、token 可配）。
func newTestServer(t *testing.T, token string) (*server, *httptest.Server) {
	t.Helper()
	t.Setenv("YOOOCLAW_HOME", t.TempDir())
	p := paths.For(paths.DefaultProfile)
	logger := NewLogger(filepath.Join(t.TempDir(), "d.log"), "info", false)
	storage := notif.NewStorage(p.Notifications, notif.PluginConfig{}, logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	srv := &server{
		ctx:               &clictx.Context{Profile: "default", Paths: p},
		cfg:               config.Default(p.Credentials),
		logger:            logger,
		st:                &runtimeState{startedAt: "2026-06-07T10:00:00Z"},
		storage:           storage,
		token:             token,
		ignored:           map[string]bool{"com.spam.app": true},
		bind:              "127.0.0.1",
		port:              18789,
		owner:             "hermes-plugin",
		generation:        "gen-1",
		executable:        "/tmp/yoooclaw",
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestServerHealth(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["server"] != "yoooclaw" {
		t.Errorf("health body: %+v", body)
	}
}

func TestServerStatusNoToken(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/daemon/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["ok"] != true || body["server"] != "yoooclaw" {
		t.Errorf("status body: %+v", body)
	}
	if body["relay"].(map[string]any)["mode"] != "standalone-http" {
		t.Errorf("expected standalone-http relay mode: %+v", body["relay"])
	}
	if body["executable"] != "/tmp/yoooclaw" {
		t.Errorf("expected executable in status: %+v", body)
	}
	lifecycle, ok := body["lifecycle"].(map[string]any)
	if !ok || lifecycle["owner"] != "hermes-plugin" || lifecycle["generation"] != "gen-1" || lifecycle["startedAt"] != "2026-06-07T10:00:00Z" {
		t.Errorf("expected lifecycle payload: %+v", body["lifecycle"])
	}
	if body["relay"].(map[string]any)["env"] == "" {
		t.Errorf("expected relay env in status: %+v", body["relay"])
	}
}

func TestServerAuthRequired(t *testing.T) {
	_, ts := newTestServer(t, "secret-token")
	// 无 token -> 401（status 不是 ingest 路径，token 非空）
	resp, err := http.Get(ts.URL + "/daemon/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 without token, got %d", resp.StatusCode)
	}
	// 带正确 token -> 200
	req, _ := http.NewRequest("GET", ts.URL+"/daemon/status", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("expected 200 with token, got %d", resp2.StatusCode)
	}
}

func TestServerIngestNotifications(t *testing.T) {
	srv, ts := newTestServer(t, "")
	payload := `{"notifications":[
		{"id":"1","app":"wechat","title":"t","body":"b","timestamp":"2026-06-07T10:00:00+08:00"},
		{"id":"2","app":"com.spam.app","title":"spam","body":"x","timestamp":"2026-06-07T10:00:00+08:00"}
	]}`
	resp, err := http.Post(ts.URL+"/notifications", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	// 第二条命中 ignoredApps，应被过滤；只 ingest 1 条
	if body["ingested"] != float64(1) {
		t.Errorf("expected 1 ingested (spam filtered): %+v", body)
	}
	if srv.st.ingestCount != 1 {
		t.Errorf("runtime ingest count = %d", srv.st.ingestCount)
	}
}

func TestServerNotFound(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/no/such/path")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestListenWithFallbackPortZeroReturnsActualPort(t *testing.T) {
	t.Parallel()
	logger := NewLogger(filepath.Join(t.TempDir(), "d.log"), "info", false)
	ln, port, err := listenWithFallback("127.0.0.1", 0, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if port == 0 {
		t.Fatal("expected actual allocated port, got 0")
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr type = %T, want *net.TCPAddr", ln.Addr())
	}
	if port != addr.Port {
		t.Fatalf("returned port = %d, listener port = %d", port, addr.Port)
	}
}

func TestServerHelpers(t *testing.T) {
	t.Parallel()
	if firstNonEmptyStr("", "", "x", "y") != "x" {
		t.Error("firstNonEmptyStr")
	}
	if nilIfEmptyStr("") != nil || nilIfEmptyStr("a") != "a" {
		t.Error("nilIfEmptyStr")
	}
	if !isIngestPath("/notifications") || isIngestPath("/daemon/status") {
		t.Error("isIngestPath")
	}
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer abc")
	if bearerValue(r) != "abc" {
		t.Error("bearerValue")
	}
	r.Header.Set("Authorization", "Basic xyz")
	if bearerValue(r) != "" {
		t.Error("non-bearer should be empty")
	}
}

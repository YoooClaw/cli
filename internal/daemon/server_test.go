package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/calendar"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/healthdata"
	"github.com/YoooClaw/cli/internal/location"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/reminders"
)

// newTestServer 构造一个最小可用的 server（无 relay、token 可配）。
func newTestServer(t *testing.T, token string) (*server, *httptest.Server) {
	t.Helper()
	t.Setenv("YOOOCLAW_HOME", t.TempDir())
	p := paths.For(paths.DefaultProfile)
	logger := NewLogger(filepath.Join(t.TempDir(), "d.log"), "info", false)
	t.Cleanup(func() { _ = logger.Close() })
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
		locationStorage:   location.NewStorage(p.Locations),
		calendarStorage:   calendar.NewStorage(p.Calendars),
		reminderStorage:   reminders.NewStorage(p.Reminders),
		healthDataStorage: healthdata.NewStorage(p.HealthData),
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

func TestRecordingPayloadUsesDisplaySizeOnly(t *testing.T) {
	t.Parallel()
	entry := recording.Entry{
		ID: "r1",
		Metadata: recording.Metadata{
			Name:            "录音",
			DurationSec:     13,
			FileSizeDisplay: "5.9 MB",
		},
	}
	for name, payload := range map[string]map[string]any{
		"list":   recordingListItem(entry),
		"detail": recordingDetail(entry),
	} {
		if payload["file_size_display"] != "5.9 MB" {
			t.Fatalf("%s file_size_display = %v", name, payload["file_size_display"])
		}
		if payload["duration_display"] != "13s" {
			t.Fatalf("%s duration_display = %v", name, payload["duration_display"])
		}
		if _, exists := payload["file_size_bytes"]; exists {
			t.Fatalf("%s must not expose removed file_size_bytes: %+v", name, payload)
		}
	}
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

func TestNotifyRecordingStatusPreservesAudioFailure(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "recordings")
	logger := NewLogger(filepath.Join(t.TempDir(), "d.log"), "info", false)
	t.Cleanup(func() { _ = logger.Close() })
	storage := recording.NewStorage(dir, logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	_, _ = storage.Ingest("r1", recording.Metadata{
		Name: "x", CreatedAt: "t", OssAudioURL: "https://oss.invalid/r1.ogg",
	}, "")
	_, _ = storage.MarkResultWritten("r1")
	const message = "音频下载失败: proxy connection refused"
	if _, err := storage.SetResultAudioFailed("r1", "https://oss.invalid/r1.ogg", message); err != nil {
		t.Fatal(err)
	}

	srv := &server{
		logger:            logger,
		recordingStorage:  storage,
		recordingEventLog: recording.NewEventLog(dir),
		egress:            NoopEgress{},
	}
	srv.notifyRecordingStatus(recording.StatusEvent{
		RecordingID:    "r1",
		TransferStatus: recording.StatusTranscribed,
		AudioStatus:    recording.AudioStatusFailed,
		Error:          message,
	})

	entry, _ := storage.FindByID("r1")
	if entry.LastError != message || entry.AudioStatus != recording.AudioStatusFailed {
		t.Fatalf("terminal transcript status masked audio failure: %+v", entry)
	}
}

// TestReloadConcurrentWithRequests 回归 /daemon/reload 与并发请求的数据竞争：
// reload 整体替换 credentialSet/tunnelSupervisor/egress，其余请求都在读。
// 由 -race 检测撕裂读写。
func TestReloadConcurrentWithRequests(t *testing.T) {
	_, ts := newTestServer(t, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			resp, err := http.Post(ts.URL+"/daemon/reload", "application/json", nil)
			if err == nil {
				resp.Body.Close()
			}
		}
	}()
	for i := 0; i < 20; i++ {
		resp, err := http.Get(ts.URL + "/daemon/status")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	<-done
}

// App 探活契约：/gateway/health 的响应字段必须与 hermes-plugin 的 req/health 对齐。
func TestGatewayHealthMatchesHermesShape(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Post(ts.URL+"/gateway/health", "application/json", strings.NewReader(`{"echo":"probe_01"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Fatalf("gateway envelope not ok: %+v", body)
	}
	if body.Data["ok"] != true {
		t.Errorf("payload.ok = %v, want true", body.Data["ok"])
	}
	if body.Data["echo"] != "probe_01" {
		t.Errorf("payload.echo = %v, want probe_01", body.Data["echo"])
	}
	for _, key := range []string{"time", "sessionDb", "lastInboundAt"} {
		if _, ok := body.Data[key]; !ok {
			t.Errorf("payload missing key %q: %+v", key, body.Data)
		}
	}
	relay, _ := body.Data["relay"].(map[string]any)
	for _, key := range []string{"stale", "currentUrl", "expectedUrl"} {
		if _, ok := relay[key]; !ok {
			t.Errorf("payload.relay missing key %q: %+v", key, relay)
		}
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
	t.Cleanup(func() { _ = logger.Close() })
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

func TestRunForegroundProxiedValidationDoesNotPublishLock(t *testing.T) {
	p := sandboxPaths(t)
	cfg := config.Default(p.Credentials)
	cfg.Relay.Enabled = false
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	ctx := &clictx.Context{Profile: p.Profile, Paths: p}

	err := RunForeground(ctx, StartOpts{IngressMode: config.IngressProxied})
	if err == nil {
		t.Fatal("proxied start without api-key should fail")
	}
	if lock := ReadLock(p); lock != nil {
		t.Fatalf("failed startup published stale lock: %+v", lock)
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

// health 探针发现 relay 地址过期时要把隧道重指到配置地址（hermes 的
// _force_reconnect_if_relay_url_stale 对应实现）。
func TestRetargetRelayIfStale(t *testing.T) {
	srv, _ := newTestServer(t, "")
	if srv.retargetRelayIfStale() {
		t.Error("没有 supervisor 时不应重指")
	}
	srv.tunnelSupervisor = relay.NewSupervisor(relay.SupervisorOptions{
		TunnelURL: "wss://stale.example.com/ws", StateDir: t.TempDir(), Logger: srv.logger,
	})
	if !srv.retargetRelayIfStale() {
		t.Error("地址过期应触发重指")
	}
	if srv.retargetRelayIfStale() {
		t.Error("地址已对齐后不应重复重指")
	}
}

func TestPollingDoesNotBlockUpdate(t *testing.T) {
	srv, _ := newTestServer(t, "")
	now := time.Now()
	srv.lastRecordingActivity.Store(now.Add(-11 * time.Minute).UnixNano())
	for i := 0; i < 20; i++ {
		for _, path := range []string{"/gateway/health", "/gateway/agents.list", "/gateway/sessions.list", "/gateway/channels.status"} {
			res := httptest.NewRecorder()
			srv.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
			if res.Code != http.StatusOK {
				t.Fatalf("%s status %d", path, res.Code)
			}
		}
	}
	// 非录音的在途请求也不参与更新空闲判断。
	srv.activeRequests.Add(1)
	defer srv.activeRequests.Add(-1)
	if !srv.updateIdle(now) {
		t.Fatal("polling or ordinary request blocked update")
	}
}

func TestRecordingWriteBlocksUpdateUntilIdle(t *testing.T) {
	srv, _ := newTestServer(t, "")
	now := time.Now()
	srv.lastRecordingActivity.Store(now.Add(-time.Hour).UnixNano())
	finish := srv.beginRecordingWrite()
	if srv.updateIdle(now.Add(time.Hour)) {
		t.Fatal("long running recording write must block update")
	}
	finish()
	if srv.updateIdle(time.Now()) {
		t.Fatal("completed recording write must start idle window")
	}
	if !srv.updateIdle(time.Now().Add(11 * time.Minute)) {
		t.Fatal("completed write never becomes idle")
	}
	// 真实录音写入入口也必须更新计时（即使参数校验失败）。
	srv.lastRecordingActivity.Store(now.Add(-time.Hour).UnixNano())
	srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/gateway/recordings.result.write", strings.NewReader(`{}`)))
	if srv.updateIdle(time.Now()) {
		t.Fatal("recording endpoint bypassed activity tracking")
	}
}

func TestRecordingDownloadBlocksUpdateAfterResponse(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	release := make(chan struct{})
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte("audio"))
	}))
	defer audio.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	body, _ := json.Marshal(map[string]any{
		"recordingId": "update_busy", "ossUrl": audio.URL + "/audio.ogg",
		"summary": map[string]any{"markdown": "test"},
	})
	response := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.result.write", "phone-a", string(body))
	if response["ok"] != true {
		t.Fatalf("write failed: %v", response)
	}
	if srv.activeRecordingWrites.Load() != 0 {
		t.Fatal("request did not finish")
	}
	srv.lastRecordingActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	if storage.ActiveDownloads() == 0 || srv.updateIdle(time.Now()) {
		t.Fatal("background download did not block update")
	}
	close(release)
	released = true
	deadline := time.Now().Add(3 * time.Second)
	for storage.ActiveDownloads() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if storage.ActiveDownloads() != 0 {
		t.Fatal("download did not complete")
	}
	if srv.updateIdle(time.Now()) {
		t.Fatal("download completion skipped idle window")
	}
	if !srv.updateIdle(time.Now().Add(11 * time.Minute)) {
		t.Fatal("completed download blocked update forever")
	}
}

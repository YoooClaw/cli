package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/version"
)

// ProtocolVersion 是手机端协议协商版本。
const ProtocolVersion = 1

// Capabilities 是本 build 支持的能力（Phase 2 标准 HTTP，relay/recordings/images 待后续）。
var Capabilities = []string{"notifications", "multi-apikey"}

// StartOpts 是 daemon 启动参数。
type StartOpts struct {
	Bind     string
	Port     int
	LogLevel string
}

type runtimeState struct {
	startedAt   string
	mu          sync.Mutex
	lastIngest  string
	ingestCount int
}

// RunForeground 前台运行 daemon 主循环（detach 子进程入口）。永不正常返回。
func RunForeground(ctx *clictx.Context, opts StartOpts) error {
	if State(ctx.Paths).Running {
		return errs.New(errs.CodeDaemonAlreadyRunning, "daemon 已在运行")
	}
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return err
	}
	bind := firstNonEmptyStr(opts.Bind, cfg.Daemon.Bind)
	port := opts.Port
	if port == 0 {
		port = cfg.Daemon.Port
	}
	logLevel := firstNonEmptyStr(opts.LogLevel, cfg.Daemon.LogLevel)
	tokenRef, _ := creds.ResolveGatewayToken(cfg)
	token := tokenRef.Value
	credentialSet := creds.ResolveAPIKeyEntries()

	loopback := bind == "127.0.0.1" || bind == "::1" || bind == "localhost"
	if !loopback && token == "" {
		return errs.New(errs.CodeUnauthorized, "绑定 "+bind+" 需要先设置 gateway token").
			WithHint("运行 yoooclaw auth token-rotate 或改回 127.0.0.1")
	}

	_ = fsutil.EnsureDir(ctx.Paths.Dir, fsutil.DirMode)
	logger := NewLogger(ctx.Paths.DaemonLog, logLevel, false)
	st := &runtimeState{startedAt: time.Now().UTC().Format(time.RFC3339)}

	storage := notif.NewStorage(ctx.Paths.Notifications, notif.PluginConfig{
		RetentionDays: cfg.Notification.RetentionDays, IgnoredApps: cfg.Notification.IgnoredApps,
	}, logger)
	if err := storage.Init(); err != nil {
		return err
	}
	ignored := map[string]bool{}
	for _, a := range cfg.Notification.IgnoredApps {
		ignored[a] = true
	}

	srv := &server{
		ctx: ctx, cfg: cfg, logger: logger, st: st, storage: storage,
		token: token, credentialSet: credentialSet, ignored: ignored, bind: bind,
	}

	// 监听；端口被占自动 +1（最多 64 次）。
	ln, actualPort, err := listenWithFallback(bind, port, logger)
	if err != nil {
		return err
	}
	srv.port = actualPort

	httpSrv := &http.Server{Handler: srv}
	if err := WriteLock(ctx.Paths, Lock{PID: os.Getpid(), StartedAt: st.startedAt, Bind: bind, Port: actualPort, LogLevel: logLevel}); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("yoooclaw daemon 启动：%s:%d（profile=%s, pid=%d）", bind, actualPort, ctx.Profile, os.Getpid()))
	if cfg.Relay.Enabled {
		logger.Warn("Relay 已启用，但本 build 暂未实现隧道（Phase 3）；当前仅直连 HTTP")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	srv.shutdown = func(reason string) {
		logger.Info("daemon 退出（" + reason + "）")
		_ = httpSrv.Close()
		RemoveLock(ctx.Paths)
		os.Exit(0)
	}

	go func() {
		<-stop
		srv.shutdown("signal")
	}()

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server 异常：" + err.Error())
		RemoveLock(ctx.Paths)
		return err
	}
	return nil
}

type server struct {
	ctx           *clictx.Context
	cfg           config.Config
	logger        *Logger
	st            *runtimeState
	storage       *notif.Storage
	token         string
	credentialSet creds.CredentialSet
	ignored       map[string]bool
	bind          string
	port          int
	shutdown      func(string)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error(fmt.Sprintf("请求处理异常：%v", rec))
			writeJSON(w, 500, map[string]any{"ok": false, "error": map[string]any{"code": "INTERNAL_ERROR", "message": fmt.Sprintf("%v", rec)}})
		}
	}()
	path := r.URL.Path

	if path == "/health" && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"server": "yoooclaw", "version": version.Version, "protocol": ProtocolVersion, "capabilities": Capabilities})
		return
	}

	authCtx, ok := s.authContext(r, path)
	if !ok {
		writeJSON(w, 401, map[string]any{"ok": false, "error": map[string]any{"code": errs.CodeUnauthorized, "message": "token 不一致或缺失"}})
		return
	}

	switch {
	case path == "/daemon/status" && r.Method == http.MethodGet:
		s.handleStatus(w)
	case path == "/daemon/stop" && r.Method == http.MethodPost:
		writeJSON(w, 200, map[string]any{"ok": true, "stopping": true})
		s.logger.Info("收到 /daemon/stop，准备优雅退出")
		go func() { time.Sleep(50 * time.Millisecond); s.shutdown("stop-endpoint") }()
	case path == "/daemon/reload" && r.Method == http.MethodPost:
		s.credentialSet = creds.ResolveAPIKeyEntries()
		writeJSON(w, 200, map[string]any{"ok": true, "running": true, "reloaded": true, "mode": s.credentialSet.Mode})
	case path == "/notifications" && r.Method == http.MethodPost:
		s.handleIngest(w, r, authCtx, "notifications")
	case path == "/gateway/notifications.push" && r.Method == http.MethodPost:
		s.handleIngest(w, r, authCtx, "items")
	case path == "/monitors" || strings.HasPrefix(path, "/monitors/"):
		s.handleMonitors(w, r, path)
	case strings.HasPrefix(path, "/tunnel/"):
		s.handleTunnel(w, r, path)
	default:
		writeJSON(w, 404, map[string]any{"ok": false, "error": map[string]any{"code": errs.CodeNotFound, "message": "未知路径：" + path}})
	}
}

type authResult struct {
	clientLabel string
	authKind    string
}

func (s *server) authContext(r *http.Request, path string) (authResult, bool) {
	bearer := bearerValue(r)
	if s.token != "" && bearer == s.token {
		return authResult{clientLabel: "local", authKind: "gateway-token"}, true
	}
	if isIngestPath(path) {
		if label := s.labelForAPIKey(bearer); label != "" {
			return authResult{clientLabel: label, authKind: "http-api-key"}, true
		}
	}
	if s.token == "" {
		return authResult{clientLabel: "local", authKind: "local"}, true
	}
	return authResult{}, false
}

func (s *server) labelForAPIKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	raw := strings.TrimPrefix(apiKey, "Bearer ")
	for _, e := range s.credentialSet.Entries {
		if strings.TrimPrefix(e.Key, "Bearer ") == raw {
			return e.Label
		}
	}
	return ""
}

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request, auth authResult, field string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "读取请求体失败"))
		return
	}
	var raw map[string]json.RawMessage
	if len(body) > 0 {
		if json.Unmarshal(body, &raw) != nil {
			writeJSON(w, 400, errBody("INVALID_PARAMS", "请求体不是合法 JSON"))
			return
		}
	}
	var items []notif.RawNotification
	// 兼容 {notifications:[]} / {items:[]} / {params:{items:[]}}。
	for _, key := range []string{field, "notifications", "items"} {
		if rawItems, ok := raw[key]; ok {
			_ = json.Unmarshal(rawItems, &items)
			break
		}
	}
	if items == nil {
		if p, ok := raw["params"]; ok {
			var params struct {
				Items []notif.RawNotification `json:"items"`
			}
			_ = json.Unmarshal(p, &params)
			items = params.Items
		}
	}
	// 过滤 ignoredApps。
	filtered := items[:0]
	for _, it := range items {
		if !s.ignored[it.App] {
			filtered = append(filtered, it)
		}
	}
	result := s.storage.Ingest(filtered, auth.clientLabel)
	s.st.mu.Lock()
	s.st.lastIngest = time.Now().UTC().Format(time.RFC3339)
	s.st.ingestCount += result.Ingested
	s.st.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"ok": true, "received": result.Received, "ingested": result.Ingested,
		"dedupedById": result.DedupedByID, "dedupedByContent": result.DedupedByContent, "invalid": result.Invalid,
	})
}

func (s *server) handleStatus(w http.ResponseWriter) {
	s.st.mu.Lock()
	lastIngest := s.st.lastIngest
	ingestCount := s.st.ingestCount
	s.st.mu.Unlock()
	var defaultLabel any
	if s.credentialSet.DefaultEntry != nil {
		defaultLabel = s.credentialSet.DefaultEntry.Label
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "server": "yoooclaw", "version": version.Version, "pid": os.Getpid(),
		"profile": s.ctx.Profile, "bind": s.bind, "port": s.port, "startedAt": s.st.startedAt,
		"lastIngestAt": nilIfEmptyStr(lastIngest), "ingestCount": ingestCount,
		"relay": map[string]any{
			"mode": "standalone-http", "connected": false, "url": s.cfg.Relay.URL, "enabled": s.cfg.Relay.Enabled,
			"note": "本 build 未实现 Relay 隧道（Phase 3），仅直连 HTTP",
		},
		"credentialMode": s.credentialSet.Mode, "defaultLabel": defaultLabel,
		"credentialWarnings": s.credentialSet.Warnings,
	})
}

// ── helpers ──

func isIngestPath(path string) bool {
	switch path {
	case "/notifications", "/recordings", "/images",
		"/gateway/notifications.push", "/gateway/recordings.sync", "/gateway/images.sync":
		return true
	}
	return false
}

func bearerValue(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

func listenWithFallback(bind string, startPort int, logger *Logger) (net.Listener, int, error) {
	port := startPort
	for attempt := 0; attempt < 64; attempt++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bind, port))
		if err == nil {
			if port != startPort {
				logger.Info(fmt.Sprintf("起始端口 %d 被占用，最终绑定到 %d", startPort, port))
			}
			return ln, port, nil
		}
		if !strings.Contains(err.Error(), "address already in use") {
			return nil, 0, err
		}
		logger.Warn(fmt.Sprintf("端口 %d 被占用，改试 %d", port, port+1))
		port++
	}
	return nil, 0, errs.New(errs.CodeUnknown, "无可用端口")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

func errBody(code, msg string) map[string]any {
	return map[string]any{"ok": false, "error": map[string]any{"code": code, "message": msg}}
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func nilIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

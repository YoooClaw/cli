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
	"github.com/YoooClaw/cli/internal/envhost"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/version"
)

// ProtocolVersion 是手机端协议协商版本。
const ProtocolVersion = 1

// Capabilities 是本 build 支持的 daemon/Relay 能力。
var Capabilities = []string{"notifications", "recordings", "images", "lightrules", "multi-apikey"}

// StartOpts 是 daemon 启动参数。
type StartOpts struct {
	Bind       string
	Port       int
	LogLevel   string
	Owner      string
	Generation string
	// IngressMode 覆盖 config.ingress.mode（standalone|proxied|direct）；空则回退 env/config。
	IngressMode string
	// EgressCallbackURL/Token 覆盖 config.ingress.egressCallback（proxied 模式出站回投）。
	EgressCallbackURL   string
	EgressCallbackToken string
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
	executable, _ := os.Executable()

	loopback := bind == "127.0.0.1" || bind == "::1" || bind == "localhost"
	if !loopback && token == "" {
		return errs.New(errs.CodeUnauthorized, "绑定 "+bind+" 需要先设置 gateway token").
			WithHint("运行 yoooclaw auth token-rotate 或改回 127.0.0.1")
	}

	_ = fsutil.EnsureDir(ctx.Paths.Dir, fsutil.DirMode)
	ctx.Paths.MigrateLogs()
	logger := NewLogger(ctx.Paths.DaemonLog, logLevel, false)
	st := &runtimeState{startedAt: time.Now().UTC().Format(time.RFC3339)}

	storage := notif.NewStorage(ctx.Paths.Notifications, notif.PluginConfig{
		RetentionDays: cfg.Notification.RetentionDays, IgnoredApps: cfg.Notification.IgnoredApps,
	}, logger)
	if err := storage.Init(); err != nil {
		return err
	}
	recordingStorage := recording.NewStorage(ctx.Paths.Recordings, logger)
	if err := recordingStorage.Init(); err != nil {
		return err
	}
	recordingEventLog := recording.NewEventLog(ctx.Paths.Recordings)
	ignored := map[string]bool{}
	for _, a := range cfg.Notification.IgnoredApps {
		ignored[a] = true
	}

	srv := &server{
		ctx: ctx, cfg: cfg, logger: logger, st: st, storage: storage,
		recordingStorage: recordingStorage, recordingEventLog: recordingEventLog,
		token:         token, credentialSet: credentialSet, ignored: ignored, bind: bind,
		owner: opts.Owner, generation: opts.Generation, executable: executable,
	}

	// 监听；端口被占自动 +1（最多 64 次）。
	ln, actualPort, err := listenWithFallback(bind, port, logger)
	if err != nil {
		return err
	}
	srv.port = actualPort

	httpSrv := &http.Server{Handler: srv}
	if err := WriteLock(ctx.Paths, Lock{
		PID: os.Getpid(), StartedAt: st.startedAt, Bind: bind, Port: actualPort, LogLevel: logLevel,
		Owner: opts.Owner, Generation: opts.Generation, Executable: executable, Version: version.Version, Profile: ctx.Profile,
	}); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("yoooclaw daemon 启动：%s:%d（profile=%s, pid=%d）", bind, actualPort, ctx.Profile, os.Getpid()))
	mode := resolveIngressMode(opts, cfg)
	srv.ingressMode = mode
	srv.egress = NoopEgress{}
	switch mode {
	case config.IngressProxied:
		// 宿主代理到手机的连接：daemon 不连隧道，只暴露 ingest API。要求 api-key 供宿主推送鉴权。
		if len(credentialSet.Entries) == 0 {
			return errs.New(errs.CodeUnauthorized, "proxied 模式需要至少一个 api-key 供宿主推送鉴权").
				WithHint("先设置 api-key，或改用 --ingress=standalone")
		}
		srv.egress = resolveProxyEgress(opts, cfg, logger)
		logger.Info("ingress=proxied：Relay 隧道关闭，等待宿主推送 ingest")
	case config.IngressDirect:
		logger.Info("ingress=direct：Relay 隧道关闭，仅接受直接 POST")
	default: // standalone
		if cfg.Relay.Enabled {
			if len(credentialSet.Entries) == 0 {
				logger.Warn("Relay 已启用但未设置 api-key；当前仅直连 HTTP")
			} else {
				srv.tunnelSupervisor = relay.NewSupervisor(relay.SupervisorOptions{
					TunnelURL:          config.ResolveRelayURL(cfg),
					HTTPBaseURL:        "http://127.0.0.1:" + fmt.Sprint(actualPort),
					HTTPToken:          token,
					HeartbeatSec:       cfg.Relay.HeartbeatSec,
					ReconnectBackoffMs: cfg.Relay.ReconnectBackoffMs,
					StateDir:           ctx.Paths.Dir,
					Logger:             logger,
				})
				result := srv.tunnelSupervisor.Apply(credentialSet)
				srv.egress = NewRelayEgress(srv.tunnelSupervisor)
				logger.Info(fmt.Sprintf("Relay tunnels applied: started=%v unchanged=%v", result.Started, result.Unchanged))
			}
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	srv.shutdown = func(reason string) {
		logger.Info("daemon 退出（" + reason + "）")
		if srv.tunnelSupervisor != nil {
			srv.tunnelSupervisor.StopAll(reason)
		}
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
	ctx               *clictx.Context
	cfg               config.Config
	logger            *Logger
	st                *runtimeState
	storage           *notif.Storage
	recordingStorage  *recording.Storage
	recordingEventLog *recording.EventLog
	tunnelSupervisor  *relay.Supervisor
	egress            Egress
	ingressMode       string
	token             string
	credentialSet     creds.CredentialSet
	ignored           map[string]bool
	bind              string
	port              int
	owner             string
	generation        string
	executable        string
	shutdown          func(string)
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
		relayResult := relay.ApplyResult{}
		if s.ingressMode == config.IngressStandalone && s.cfg.Relay.Enabled {
			if s.tunnelSupervisor == nil && len(s.credentialSet.Entries) > 0 {
				s.tunnelSupervisor = relay.NewSupervisor(relay.SupervisorOptions{
					TunnelURL:          config.ResolveRelayURL(s.cfg),
					HTTPBaseURL:        "http://127.0.0.1:" + fmt.Sprint(s.port),
					HTTPToken:          s.token,
					HeartbeatSec:       s.cfg.Relay.HeartbeatSec,
					ReconnectBackoffMs: s.cfg.Relay.ReconnectBackoffMs,
					StateDir:           s.ctx.Paths.Dir,
					Logger:             s.logger,
				})
			}
			if s.tunnelSupervisor != nil {
				relayResult = s.tunnelSupervisor.Apply(s.credentialSet)
			}
		}
		writeJSON(w, 200, map[string]any{
			"ok": true, "running": true, "reloaded": true, "mode": s.credentialSet.Mode,
			"defaultLabel": defaultEntryLabel(s.credentialSet), "warnings": s.credentialSet.Warnings,
			"started": relayResult.Started, "stopped": relayResult.Stopped,
			"restarted": relayResult.Restarted, "unchanged": relayResult.Unchanged,
		})
	case path == "/notifications" && r.Method == http.MethodPost:
		s.handleIngest(w, r, authCtx, "notifications")
	case path == "/gateway/notifications.push" && r.Method == http.MethodPost:
		s.handleIngest(w, r, authCtx, "items")
	case strings.HasPrefix(path, "/gateway/recordings.") && r.Method == http.MethodPost:
		s.handleRecordingGateway(w, r, path)
	case path == "/images" && r.Method == http.MethodPost:
		s.handleImageHTTP(w, r, authCtx)
	case path == "/gateway/images.sync" && r.Method == http.MethodPost:
		s.handleImageGateway(w, r, authCtx)
	case path == "/monitors" || strings.HasPrefix(path, "/monitors/"):
		s.handleMonitors(w, r, path)
	case path == "/light/send" && r.Method == http.MethodPost:
		s.handleLightSend(w, r)
	case strings.HasPrefix(path, "/gateway/lightrules."):
		s.handleLightrules(w, r, path)
	case strings.HasPrefix(path, "/gateway/") && r.Method == http.MethodPost:
		s.handleGatewayCompat(w, r, path)
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
	internalRelay := r.Header.Get(relay.InternalHTTPHeader) == "1"
	internalLabel := strings.TrimSpace(r.Header.Get(relay.InternalClientLabelHeader))
	gatewayTokenOK := s.token == "" || bearer == s.token
	if internalRelay && internalLabel != "" && gatewayTokenOK {
		return authResult{clientLabel: internalLabel, authKind: "relay-api-key"}, true
	}
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
	s.recordIngest(result)
	writeJSON(w, 200, map[string]any{
		"ok": true, "received": result.Received, "ingested": result.Ingested,
		"dedupedById": result.DedupedByID, "dedupedByContent": result.DedupedByContent, "invalid": result.Invalid,
	})
}

func (s *server) recordIngest(result notif.IngestResult) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	s.st.lastIngest = time.Now().UTC().Format(time.RFC3339)
	s.st.ingestCount += result.Ingested
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
	relayStatus := s.relayStatusPayload()
	writeJSON(w, 200, map[string]any{
		"ok": true, "server": "yoooclaw", "version": version.Version, "pid": os.Getpid(),
		"executable": nilIfEmptyStr(s.executable),
		"profile":    s.ctx.Profile, "bind": s.bind, "port": s.port, "startedAt": s.st.startedAt,
		"lifecycle": map[string]any{
			"owner":      nilIfEmptyStr(s.owner),
			"generation": nilIfEmptyStr(s.generation),
			"startedAt":  s.st.startedAt,
		},
		"lastIngestAt": nilIfEmptyStr(lastIngest), "ingestCount": ingestCount,
		"ingressMode":    s.ingressMode,
		"relay":          relayStatus,
		"tunnels":        relayStatus["tunnels"],
		"credentialMode": s.credentialSet.Mode, "defaultLabel": defaultLabel,
		"credentialWarnings": s.credentialSet.Warnings,
	})
}

func (s *server) relayStatusPayload() map[string]any {
	if s.tunnelSupervisor != nil {
		status := s.tunnelSupervisor.Status()
		connected := false
		reconnectAttempt := 0
		lastDisconnectReason := ""
		for _, tunnel := range status.Tunnels {
			if tunnel.Default || status.DefaultLabel == "" {
				connected = tunnel.Connected
				reconnectAttempt = tunnel.ReconnectAttempt
				lastDisconnectReason = tunnel.LastDisconnectReason
				break
			}
		}
		note := any(nil)
		if !connected {
			note = "Relay 重连中"
		}
		return map[string]any{
			"mode": "relay", "connected": connected, "env": envhost.Name(), "url": config.ResolveRelayURL(s.cfg), "enabled": s.cfg.Relay.Enabled,
			"reconnectAttempt": reconnectAttempt, "lastDisconnectReason": nilIfEmptyStr(lastDisconnectReason),
			"note": note, "tunnels": status.Tunnels,
		}
	}
	note := "Relay 未启用，走直连 HTTP"
	switch s.ingressMode {
	case config.IngressProxied:
		note = "ingress=proxied：隧道由宿主代理，daemon 仅收 ingest"
	case config.IngressDirect:
		note = "ingress=direct：隧道关闭，仅接受直接 POST"
	default:
		if s.cfg.Relay.Enabled {
			note = "Relay 已启用但当前 CredentialSet 没有可用 api-key"
		}
	}
	return map[string]any{
		"mode": "standalone-http", "connected": false, "env": envhost.Name(), "url": config.ResolveRelayURL(s.cfg), "enabled": s.cfg.Relay.Enabled,
		"reconnectAttempt": 0, "note": note, "tunnels": []any{},
	}
}

// ── helpers ──

func isIngestPath(path string) bool {
	switch path {
	case "/notifications", "/images",
		"/gateway/notifications.push", "/gateway/recordings.result.write", "/gateway/images.sync":
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
			actualPort := listenerPort(ln, port)
			if startPort == 0 {
				logger.Info(fmt.Sprintf("系统分配 daemon 端口 %d", actualPort))
			} else if actualPort != startPort {
				logger.Info(fmt.Sprintf("起始端口 %d 被占用，最终绑定到 %d", startPort, actualPort))
			}
			return ln, actualPort, nil
		}
		if !strings.Contains(err.Error(), "address already in use") {
			return nil, 0, err
		}
		logger.Warn(fmt.Sprintf("端口 %d 被占用，改试 %d", port, port+1))
		port++
	}
	return nil, 0, errs.New(errs.CodeUnknown, "无可用端口")
}

func listenerPort(ln net.Listener, fallback int) int {
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return fallback
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

// resolveIngressMode 解析 ingress 模式，优先级：flag > env(YOOOCLAW_INGRESS) > config > standalone。
func resolveIngressMode(opts StartOpts, cfg config.Config) string {
	raw := firstNonEmptyStr(opts.IngressMode, os.Getenv("YOOOCLAW_INGRESS"), cfg.Ingress.Mode)
	return config.NormalizeIngressMode(raw)
}

// resolveProxyEgress 装配 proxied 模式出站端口；未配置回调则丢弃并告警。
func resolveProxyEgress(opts StartOpts, cfg config.Config, logger *Logger) Egress {
	url := firstNonEmptyStr(opts.EgressCallbackURL, cfg.Ingress.EgressCallback.URL)
	token := firstNonEmptyStr(opts.EgressCallbackToken, cfg.Ingress.EgressCallback.Token)
	if url == "" {
		logger.Warn("proxied 模式未配置 egress 回调，出站事件将被丢弃")
		return NoopEgress{}
	}
	logger.Info("ingress=proxied：出站事件回投 " + url)
	return NewProxyEgress(url, token, logger)
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

func defaultEntryLabel(set creds.CredentialSet) any {
	if set.DefaultEntry == nil {
		return nil
	}
	return set.DefaultEntry.Label
}

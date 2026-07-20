package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	mode := resolveIngressMode(opts, cfg)

	loopback := bind == "127.0.0.1" || bind == "::1" || bind == "localhost"
	if !loopback && token == "" {
		return errs.New(errs.CodeUnauthorized, "绑定 "+bind+" 需要先设置 gateway token").
			WithHint("运行 yoooclaw auth token-rotate 或改回 127.0.0.1")
	}

	_ = fsutil.EnsureDir(ctx.Paths.Dir, fsutil.DirMode)
	// OS 级单例互斥。仅靠 daemon.lock 的 check-then-write 有竞态：两个 daemon
	// 并发启动时端口 +1 回退让双方都能起来，后者的 lock 覆盖前者，之后退出时
	// 再互删对方的锁。flock 拿不到直接拒绝启动；获取本身出错（受限文件系统等）
	// 按未持有处理，退回原有 lock 文件语义，不把 daemon 卡死。
	releaseSingleton, lockErr := acquireProcessLock(filepath.Join(ctx.Paths.Dir, daemonSingletonName))
	if errors.Is(lockErr, errProcessLockHeld) {
		return errs.New(errs.CodeDaemonAlreadyRunning, "另一个 daemon 进程持有单例锁，拒绝重复启动")
	}
	if releaseSingleton != nil {
		defer releaseSingleton()
	}
	ctx.Paths.MigrateLogs()
	logger := NewLogger(ctx.Paths.DaemonLog, logLevel, false)
	st := &runtimeState{startedAt: time.Now().UTC().Format(time.RFC3339)}
	// Validate all prerequisites before publishing daemon.lock. Previously a
	// proxied start without an api-key wrote the lock and then exited, leaving
	// callers (especially after WSL restarts) talking to a dead or reused PID.
	if mode == config.IngressProxied && len(credentialSet.Entries) == 0 {
		return errs.New(errs.CodeUnauthorized, "proxied 模式需要至少一个 api-key 供宿主推送鉴权").
			WithHint("先设置 api-key，或改用 --ingress=standalone")
	}
	// standalone daemon 与 hermes 插件的存储写者锁互斥（daemonless plugin
	// mode）；proxied 模式豁免——那是插件自己托管的 daemon。
	if mode != config.IngressProxied {
		if err := checkWriterLock(ctx.Paths); err != nil {
			return err
		}
	}

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
		token: token, credentialSet: credentialSet, ignored: ignored, bind: bind,
		owner: opts.Owner, generation: opts.Generation, executable: executable,
		ingressMode: mode, egress: NoopEgress{},
	}
	if mode == config.IngressProxied {
		srv.egress = resolveProxyEgress(opts, cfg, logger)
	}

	// 监听；端口被占自动 +1（最多 64 次）。
	ln, actualPort, err := listenWithFallback(bind, port, logger)
	if err != nil {
		return err
	}
	defer ln.Close()
	srv.port = actualPort

	// ReadHeaderTimeout/IdleTimeout 防止半开连接把 goroutine/fd 无限攒下去
	// （长期运行后 accept 堆死是「daemon 还在但没响应」的典型来源）。
	// 不设 WriteTimeout/ReadTimeout：慢速手机端上传大 payload 是合法场景，
	// 请求体大小由 ServeHTTP 里的 MaxBytesReader 兜底。
	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := WriteLock(ctx.Paths, Lock{
		PID: os.Getpid(), StartedAt: st.startedAt, Bind: bind, Port: actualPort, LogLevel: logLevel,
		Owner: opts.Owner, Generation: opts.Generation, Executable: executable, Version: version.Version, Profile: ctx.Profile,
	}); err != nil {
		return err
	}
	// Every non-os.Exit return path must retire the lock. This includes startup
	// failures and unexpected http.Server termination. Removal is PID-guarded:
	// a lingering old daemon must never delete a newer daemon's lock.
	selfPID := os.Getpid()
	defer RemoveLockIfOwnedBy(ctx.Paths, selfPID)
	logger.Info(fmt.Sprintf("yoooclaw daemon 启动：%s:%d（profile=%s, pid=%d）", bind, actualPort, ctx.Profile, os.Getpid()))
	switch mode {
	case config.IngressProxied:
		// 宿主代理到手机的连接：daemon 不连隧道，只暴露 ingest API。
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
		if sup := srv.supervisor(); sup != nil {
			sup.StopAll(reason)
		}
		// 给 in-flight 请求 3 秒收尾（正在落盘的 ingest 批次能写完），
		// 超时再硬关，保证退出不会被慢客户端拖死。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			_ = httpSrv.Close()
		}
		RemoveLockIfOwnedBy(ctx.Paths, selfPID)
		os.Exit(0)
	}

	go func() {
		<-stop
		srv.shutdown("signal")
	}()

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server 异常：" + err.Error())
		RemoveLockIfOwnedBy(ctx.Paths, selfPID)
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
	ingressMode       string
	token             string
	ignored           map[string]bool
	bind              string
	port              int
	owner             string
	generation        string
	executable        string
	shutdown          func(string)

	// shareMu 保护下面三个字段：/daemon/reload 会在请求 goroutine 里整体替换它们，
	// 而每个并发请求都在读（鉴权、status、egress 推送）。
	shareMu          sync.RWMutex
	credentialSet    creds.CredentialSet
	tunnelSupervisor *relay.Supervisor
	egress           Egress
}

func (s *server) snapshotCreds() creds.CredentialSet {
	s.shareMu.RLock()
	defer s.shareMu.RUnlock()
	return s.credentialSet
}

func (s *server) supervisor() *relay.Supervisor {
	s.shareMu.RLock()
	defer s.shareMu.RUnlock()
	return s.tunnelSupervisor
}

func (s *server) currentEgress() Egress {
	s.shareMu.RLock()
	defer s.shareMu.RUnlock()
	return s.egress
}

// maxRequestBodyBytes 是单请求体上限。最大的合法 payload 是 base64 图片同步
// （config.image.maxBytes 默认 20MB，base64 膨胀 ~1.37x），64MB 足够宽裕；
// 没有上限的话一个恶意/异常的大 POST 就能把 daemon 直接 OOM。
const maxRequestBodyBytes = 64 << 20

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error(fmt.Sprintf("请求处理异常：%v", rec))
			writeJSON(w, 500, map[string]any{"ok": false, "error": map[string]any{"code": "INTERNAL_ERROR", "message": fmt.Sprintf("%v", rec)}})
		}
	}()
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	}
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
		set, relayResult := s.reloadCredentials()
		writeJSON(w, 200, map[string]any{
			"ok": true, "running": true, "reloaded": true, "mode": set.Mode,
			"defaultLabel": defaultEntryLabel(set), "warnings": set.Warnings,
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

// reloadCredentials 重读凭据并增量刷新隧道。持 shareMu 写锁做整体替换；
// supervisor.Apply 里的网络操作是非阻塞的（隧道在各自 goroutine 建连），
// 不会长时间占住写锁。
func (s *server) reloadCredentials() (creds.CredentialSet, relay.ApplyResult) {
	set := creds.ResolveAPIKeyEntries()
	relayResult := relay.ApplyResult{}
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	s.credentialSet = set
	if s.ingressMode == config.IngressStandalone && s.cfg.Relay.Enabled {
		if s.tunnelSupervisor == nil && len(set.Entries) > 0 {
			s.tunnelSupervisor = relay.NewSupervisor(relay.SupervisorOptions{
				TunnelURL:          config.ResolveRelayURL(s.cfg),
				HTTPBaseURL:        "http://127.0.0.1:" + fmt.Sprint(s.port),
				HTTPToken:          s.token,
				HeartbeatSec:       s.cfg.Relay.HeartbeatSec,
				ReconnectBackoffMs: s.cfg.Relay.ReconnectBackoffMs,
				StateDir:           s.ctx.Paths.Dir,
				Logger:             s.logger,
			})
			// 启动时没有 api-key 的话 egress 是 Noop；补上隧道后出站事件
			// 必须跟着切到 Relay，否则 recording.status 等推送会一直被丢弃。
			s.egress = NewRelayEgress(s.tunnelSupervisor)
		}
		if s.tunnelSupervisor != nil {
			relayResult = s.tunnelSupervisor.Apply(set)
		}
	}
	return set, relayResult
}

type authResult struct {
	clientLabel string
	authKind    string
}

func (s *server) authContext(r *http.Request, path string) (authResult, bool) {
	bearer := bearerValue(r)
	internalRelay := r.Header.Get(relay.InternalHTTPHeader) == "1"
	internalLabel := strings.TrimSpace(r.Header.Get(relay.InternalClientLabelHeader))
	gatewayTokenOK := s.token == "" || secretEqual(bearer, s.token)
	if internalRelay && internalLabel != "" && gatewayTokenOK {
		return authResult{clientLabel: internalLabel, authKind: "relay-api-key"}, true
	}
	if s.token != "" && secretEqual(bearer, s.token) {
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
	for _, e := range s.snapshotCreds().Entries {
		if secretEqual(strings.TrimPrefix(e.Key, "Bearer "), raw) {
			return e.Label
		}
	}
	return ""
}

// secretEqual 常数时间比较 token/api-key，避免逐字节短路造成 timing 侧信道
//（daemon 可经 Relay/非 loopback 暴露，鉴权比较不能用 ==）。
func secretEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
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
		"ok": result.Failed == 0, "received": result.Received, "ingested": result.Ingested,
		"dedupedById": result.DedupedByID, "dedupedByContent": result.DedupedByContent, "invalid": result.Invalid,
		"failed": result.Failed,
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
	set := s.snapshotCreds()
	var defaultLabel any
	if set.DefaultEntry != nil {
		defaultLabel = set.DefaultEntry.Label
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
		"credentialMode": set.Mode, "defaultLabel": defaultLabel,
		"credentialWarnings": set.Warnings,
	})
}

func (s *server) relayStatusPayload() map[string]any {
	if sup := s.supervisor(); sup != nil {
		status := sup.Status()
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
		// errno 判断为主（Windows 的报错文案是 "Only one usage of each socket
		// address"，字符串匹配不到会让端口回退整个失效），字符串匹配只作兜底。
		if !isAddrInUse(err) && !strings.Contains(err.Error(), "address already in use") {
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

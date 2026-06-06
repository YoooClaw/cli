package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/gorilla/websocket"
)

// Credential 是 Relay WS 建连凭据。
type Credential struct {
	Query   map[string]string
	Headers map[string]string
}

// CredentialProvider 每次重连时提供最新凭据。
type CredentialProvider func() (Credential, error)

// ClientOptions 是 RelayClient 配置。
type ClientOptions struct {
	TunnelURL          string
	CredentialProvider CredentialProvider
	HeartbeatSec       int
	ReconnectBackoffMs int
	StatusFilePath     string
	Logger             Logger
}

// Client 负责 Relay WebSocket 建连、心跳、重连与 frame 分发。
type Client struct {
	opts ClientOptions

	mu        sync.Mutex
	conn      *websocket.Conn
	writeMu   sync.Mutex
	stopCh    chan struct{}
	stoppedCh chan struct{}
	stopOnce  sync.Once

	handlersMu           sync.Mutex
	inboundHandlers      []func(Frame)
	connectedHandlers    []func()
	disconnectedHandlers []func(string)

	stateMu              sync.Mutex
	connected            bool
	reconnectAttempt     int
	lastDisconnectReason string
	lastInboundAt        time.Time
}

// NewClient 构造 Relay client。
func NewClient(opts ClientOptions) *Client {
	if opts.HeartbeatSec <= 0 {
		opts.HeartbeatSec = defaultHeartbeatSec
	}
	if opts.ReconnectBackoffMs <= 0 {
		opts.ReconnectBackoffMs = defaultReconnectBackoffMs
	}
	return &Client{opts: opts, stopCh: make(chan struct{}), stoppedCh: make(chan struct{})}
}

// OnInbound 注册入站 frame handler。
func (c *Client) OnInbound(handler func(Frame)) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.inboundHandlers = append(c.inboundHandlers, handler)
}

// OnConnected 注册连接成功回调。
func (c *Client) OnConnected(handler func()) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.connectedHandlers = append(c.connectedHandlers, handler)
}

// OnDisconnected 注册断开回调。
func (c *Client) OnDisconnected(handler func(string)) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.disconnectedHandlers = append(c.disconnectedHandlers, handler)
}

// Start 非阻塞启动自动重连循环。
func (c *Client) Start() {
	go c.run()
}

func (c *Client) run() {
	defer close(c.stoppedCh)
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}
		err := c.connectOnce()
		select {
		case <-c.stopCh:
			return
		default:
		}
		reason := "unknown"
		if err != nil {
			reason = err.Error()
		}
		c.setDisconnected(reason)
		c.emitDisconnected(reason)
		delay := c.nextReconnectDelay()
		c.opts.Logger.Info(fmt.Sprintf("Relay tunnel: reconnecting in %s (attempt %d)", delay, c.Status().ReconnectAttempt))
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-c.stopCh:
			timer.Stop()
			return
		}
	}
}

func (c *Client) connectOnce() error {
	credential, err := c.opts.CredentialProvider()
	if err != nil {
		return fmt.Errorf("credential-error: %w", err)
	}
	wsURL, headers, err := c.buildURLAndHeaders(credential)
	if err != nil {
		return err
	}
	c.opts.Logger.Info("Relay tunnel: connecting to " + redactURL(wsURL.String()))
	c.writeStatus("connecting", "")

	dialer := websocket.Dialer{HandshakeTimeout: handshakeTimeout}
	connCh := make(chan struct {
		conn *websocket.Conn
		resp *http.Response
		err  error
	}, 1)
	go func() {
		conn, resp, err := dialer.Dial(wsURL.String(), headers)
		connCh <- struct {
			conn *websocket.Conn
			resp *http.Response
			err  error
		}{conn: conn, resp: resp, err: err}
	}()
	var result struct {
		conn *websocket.Conn
		resp *http.Response
		err  error
	}
	select {
	case result = <-connCh:
	case <-time.After(connectWatchdog):
		return fmt.Errorf("connect-watchdog-timeout")
	case <-c.stopCh:
		return fmt.Errorf("stopped")
	}
	if result.err != nil {
		status := ""
		if result.resp != nil {
			status = result.resp.Status
		}
		if status != "" {
			return fmt.Errorf("websocket dial failed: %s (%s)", result.err.Error(), status)
		}
		return fmt.Errorf("websocket dial failed: %w", result.err)
	}
	conn := result.conn
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	c.setConnected()
	c.emitConnected()
	c.writeStatus("connected", "")
	c.opts.Logger.Info("Relay tunnel: connected, heartbeat started")

	readDone := make(chan error, 1)
	heartbeatDone := make(chan error, 1)
	conn.SetPongHandler(func(_ string) error {
		c.markInbound()
		c.opts.Logger.Info("Relay tunnel: <- pong received")
		return nil
	})
	go c.readLoop(conn, readDone)
	go c.heartbeatLoop(conn, heartbeatDone)

	select {
	case err := <-readDone:
		return err
	case err := <-heartbeatDone:
		return err
	case <-c.stopCh:
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client stopping"), time.Now().Add(time.Second))
		return fmt.Errorf("stopped")
	}
}

func (c *Client) buildURLAndHeaders(credential Credential) (*url.URL, http.Header, error) {
	wsURL, err := url.Parse(c.opts.TunnelURL)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range credential.Query {
		if wsURL.Query().Get(k) == "" {
			q := wsURL.Query()
			q.Set(k, v)
			wsURL.RawQuery = q.Encode()
		}
	}
	headers := http.Header{}
	for k, v := range credential.Headers {
		headers.Set(k, v)
	}
	return wsURL, headers, nil
}

func (c *Client) readLoop(conn *websocket.Conn, done chan<- error) {
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			done <- fmt.Errorf("read failed: %w", err)
			return
		}
		c.markInbound()
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		text := string(data)
		if text == "pong" {
			continue
		}
		c.opts.Logger.Info(fmt.Sprintf("Relay tunnel: received message (%d chars): %s", len(text), preview(text, 500)))
		var frame Frame
		if err := json.Unmarshal(data, &frame); err != nil {
			c.opts.Logger.Warn("Relay tunnel: received invalid frame, ignoring")
			continue
		}
		c.handlersMu.Lock()
		handlers := append([]func(Frame){}, c.inboundHandlers...)
		c.handlersMu.Unlock()
		for _, handler := range handlers {
			h := handler
			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						c.opts.Logger.Error(fmt.Sprintf("Relay tunnel: handler panic: %v", rec))
					}
				}()
				h(frame)
			}()
		}
	}
}

func (c *Client) heartbeatLoop(conn *websocket.Conn, done chan<- error) {
	interval := time.Duration(c.opts.HeartbeatSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := c.sendHeartbeat(conn); err != nil {
		done <- err
		return
	}
	for {
		select {
		case <-ticker.C:
			if err := c.sendHeartbeat(conn); err != nil {
				done <- err
				return
			}
		case <-c.stopCh:
			done <- fmt.Errorf("stopped")
			return
		}
	}
}

func (c *Client) sendHeartbeat(conn *websocket.Conn) error {
	timeout := time.Duration(c.opts.HeartbeatSec*3) * time.Second
	if idle := time.Since(c.lastInbound()); idle >= timeout {
		_ = conn.Close()
		return fmt.Errorf("heartbeat-timeout idleMs=%d", idle.Milliseconds())
	}
	c.opts.Logger.Info(`Relay tunnel: -> heartbeat "ping"`)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		_ = conn.Close()
		return fmt.Errorf("heartbeat text ping failed: %w", err)
	}
	if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("heartbeat control ping failed: %w", err)
	}
	return nil
}

// Send sends a JSON frame.
func (c *Client) Send(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		c.opts.Logger.Warn("Relay tunnel: marshal frame failed: " + err.Error())
		return
	}
	c.SendRaw(string(payload))
}

// SendRaw 原样发送文本到 Relay。
func (c *Client) SendRaw(text string) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		c.opts.Logger.Warn("Relay tunnel: sendRaw skipped, ws not open")
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		c.opts.Logger.Warn("Relay tunnel: sendRaw failed: " + err.Error())
		_ = conn.Close()
	}
}

// Stop 优雅停止客户端。
func (c *Client) Stop(reason string) {
	c.stopOnce.Do(func() {
		close(c.stopCh)
		c.mu.Lock()
		conn := c.conn
		c.conn = nil
		c.mu.Unlock()
		if conn != nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason), time.Now().Add(time.Second))
			_ = conn.Close()
		}
		c.writeStatus("stopped", "")
	})
	<-c.stoppedCh
}

// IsConnected 报告是否已连接。
func (c *Client) IsConnected() bool {
	return c.Status().Connected
}

// Status 返回连接状态。
func (c *Client) Status() Status {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return Status{
		Connected:            c.connected,
		URL:                  c.opts.TunnelURL,
		ReconnectAttempt:     c.reconnectAttempt,
		LastDisconnectReason: c.lastDisconnectReason,
	}
}

func (c *Client) setConnected() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.connected = true
	c.reconnectAttempt = 0
	c.lastDisconnectReason = ""
	c.lastInboundAt = time.Now()
}

func (c *Client) setDisconnected(reason string) {
	c.mu.Lock()
	c.conn = nil
	c.mu.Unlock()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.connected = false
	c.lastDisconnectReason = reason
}

func (c *Client) nextReconnectDelay() time.Duration {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	delay := time.Duration(c.opts.ReconnectBackoffMs) * time.Millisecond * time.Duration(1<<min(c.reconnectAttempt, 10))
	if delay > time.Minute {
		delay = time.Minute
	}
	c.reconnectAttempt++
	return delay
}

func (c *Client) markInbound() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.lastInboundAt = time.Now()
}

func (c *Client) lastInbound() time.Time {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.lastInboundAt
}

func (c *Client) emitConnected() {
	c.handlersMu.Lock()
	handlers := append([]func(){}, c.connectedHandlers...)
	c.handlersMu.Unlock()
	for _, h := range handlers {
		go h()
	}
}

func (c *Client) emitDisconnected(reason string) {
	c.writeStatus("disconnected", reason)
	c.handlersMu.Lock()
	handlers := append([]func(string){}, c.disconnectedHandlers...)
	c.handlersMu.Unlock()
	for _, h := range handlers {
		handler := h
		go handler(reason)
	}
}

func (c *Client) writeStatus(state, reason string) {
	if c.opts.StatusFilePath == "" {
		return
	}
	payload := map[string]any{
		"state":            state,
		"since":            time.Now().UTC().Format(time.RFC3339Nano),
		"reconnectAttempt": c.Status().ReconnectAttempt,
	}
	if reason != "" {
		payload["lastDisconnectReason"] = reason
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	_ = fsutil.EnsureDir(filepath.Dir(c.opts.StatusFilePath), fsutil.DirMode)
	_ = os.WriteFile(c.opts.StatusFilePath, append(data, '\n'), 0o600)
}

func relayCredential(apiKey string) Credential {
	raw := strings.TrimPrefix(strings.TrimSpace(apiKey), "Bearer ")
	return Credential{Query: map[string]string{"apiKey": raw}}
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if v := u.Query().Get("apiKey"); v != "" {
		q := u.Query()
		if len(v) > 8 {
			q.Set("apiKey", v[:8]+"...")
		} else {
			q.Set("apiKey", "...")
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func preview(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

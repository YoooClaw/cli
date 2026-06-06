package relay

import "time"

const (
	// InternalHTTPHeader 标记 Relay loopback 请求，daemon 用它保留真实 client label。
	InternalHTTPHeader = "x-openclaw-relay-internal"
	// InternalClientLabelHeader 是当前隧道对应的 api-key label。
	InternalClientLabelHeader = "x-yoooclaw-internal-client-label"
)

const (
	defaultHeartbeatSec       = 10
	defaultReconnectBackoffMs = 2000
	handshakeTimeout          = 15 * time.Second
	connectWatchdog           = 20 * time.Second
)

// Logger 是 relay 包依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
	Error(string)
}

// Frame 是 Relay JSON frame。
type Frame map[string]any

// RPCResponseFrame 是 Gateway RPC 响应帧：{type,id,ok,payload?,error?}。
type RPCResponseFrame struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Payload any    `json:"payload,omitempty"`
	Error   any    `json:"error,omitempty"`
}

// ProxyResponseFrame 是 HTTP 代理响应帧。
type ProxyResponseFrame struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body,omitempty"`
}

// StreamFrame 是流式响应片段帧。
type StreamFrame struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	State string `json:"state"`
	Data  string `json:"data"`
}

// ProxyErrorFrame 是 HTTP 代理错误帧。
type ProxyErrorFrame struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// WSOpenedFrame 是 WS 子通道打开确认。
type WSOpenedFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// WSDataFrame 是 WS 子通道数据帧。
type WSDataFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Data string `json:"data"`
}

// WSCloseFrame 是 WS 子通道关闭帧。
type WSCloseFrame struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// EventFrame 是 daemon 主动推送事件帧。
type EventFrame struct {
	Type    string `json:"type"`
	Event   string `json:"event"`
	Payload any    `json:"payload"`
}

// Status 是单条隧道状态。
type Status struct {
	Connected            bool
	URL                  string
	ReconnectAttempt     int
	LastDisconnectReason string
}

// TunnelStatus 是 supervisor 对外暴露的单隧道状态。
type TunnelStatus struct {
	Label                string `json:"label"`
	Default              bool   `json:"default"`
	Connected            bool   `json:"connected"`
	URL                  string `json:"url"`
	ReconnectAttempt     int    `json:"reconnectAttempt"`
	LastDisconnectReason string `json:"lastDisconnectReason,omitempty"`
}

// SupervisorStatus 是全部隧道状态。
type SupervisorStatus struct {
	Mode         string         `json:"mode"`
	DefaultLabel string         `json:"defaultLabel"`
	Tunnels      []TunnelStatus `json:"tunnels"`
}

// ApplyResult 是 credential set 增量应用结果。
type ApplyResult struct {
	Started   []string `json:"started"`
	Stopped   []string `json:"stopped"`
	Restarted []string `json:"restarted"`
	Unchanged []string `json:"unchanged"`
}

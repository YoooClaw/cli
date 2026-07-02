package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/YoooClaw/cli/internal/relay"
)

// Egress 是 daemon 出站事件端口（core → 手机）。不同传输模式有不同实现：
// standalone 走 Relay 隧道；proxied 回投宿主 HTTP 回调；direct 丢弃。
// 见 docs/design/ingress-layering.md。
type Egress interface {
	PushEvent(event string, payload any) error
}

// RelayEgress 把事件经 Relay 隧道广播给手机端（standalone 模式）。
type RelayEgress struct {
	sup *relay.Supervisor
}

// NewRelayEgress 构造基于隧道 supervisor 的出站端口。
func NewRelayEgress(sup *relay.Supervisor) *RelayEgress {
	return &RelayEgress{sup: sup}
}

// PushEvent 经隧道广播事件。
func (e *RelayEgress) PushEvent(event string, payload any) error {
	if e.sup == nil {
		return nil
	}
	e.sup.PushEvent(event, payload)
	return nil
}

// ProxyEgress 把事件 POST 给宿主 egress 回调（proxied 模式），由宿主再转发给手机端。
type ProxyEgress struct {
	url    string
	token  string
	client *http.Client
	logger *Logger
}

// NewProxyEgress 构造回投宿主回调的出站端口。
func NewProxyEgress(url, token string, logger *Logger) *ProxyEgress {
	return &ProxyEgress{
		url:    url,
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

// PushEvent 异步把 {event, payload} POST 给宿主回调。请求会保留自身的 10s
// 超时，但调用方只负责排队，不会被宿主回调的延迟阻塞 ingest 响应。
func (e *ProxyEgress) PushEvent(event string, payload any) error {
	body, err := json.Marshal(map[string]any{"event": event, "payload": payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	go e.deliver(event, req)
	return nil
}

func (e *ProxyEgress) deliver(event string, req *http.Request) {
	resp, err := e.client.Do(req)
	if err != nil {
		if e.logger != nil {
			e.logger.Warn("egress 回投失败 event=" + event + ": " + err.Error())
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("egress 回调返回 %d event=%s", resp.StatusCode, event)
		if e.logger != nil {
			e.logger.Warn(msg)
		}
		return
	}
}

// NoopEgress 丢弃出站事件（direct 模式，或 proxied 未配置回调时）。
type NoopEgress struct{}

// PushEvent 丢弃事件。
func (NoopEgress) PushEvent(string, any) error { return nil }

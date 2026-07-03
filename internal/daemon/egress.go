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

// proxyEgressRetryDelays 是回投失败后的重试间隔。宿主插件可能正在整组重启
// （回调服务短暂不可达），带退避多试几次能把重启窗口内的事件送达而不是丢掉。
var proxyEgressRetryDelays = []time.Duration{time.Second, 3 * time.Second, 9 * time.Second}

// PushEvent 异步把 {event, payload} POST 给宿主回调。请求会保留自身的 10s
// 超时，但调用方只负责排队，不会被宿主回调的延迟阻塞 ingest 响应。
func (e *ProxyEgress) PushEvent(event string, payload any) error {
	body, err := json.Marshal(map[string]any{"event": event, "payload": payload})
	if err != nil {
		return err
	}
	go e.deliver(event, body)
	return nil
}

// deliver 投递事件，网络错误与 5xx 按 proxyEgressRetryDelays 重试；4xx 属
// 配置性失败（token 不符、路径不对），重试无意义，立即放弃。
func (e *ProxyEgress) deliver(event string, body []byte) {
	for attempt := 0; ; attempt++ {
		retryable, err := e.post(body)
		if err == nil {
			return
		}
		if !retryable || attempt >= len(proxyEgressRetryDelays) {
			if e.logger != nil {
				e.logger.Warn(fmt.Sprintf("egress 回投失败（放弃）event=%s attempts=%d: %v", event, attempt+1, err))
			}
			return
		}
		delay := proxyEgressRetryDelays[attempt]
		if e.logger != nil {
			e.logger.Warn(fmt.Sprintf("egress 回投失败 event=%s attempt=%d，%s 后重试: %v", event, attempt+1, delay, err))
		}
		time.Sleep(delay)
	}
}

func (e *ProxyEgress) post(body []byte) (retryable bool, err error) {
	req, err := http.NewRequest(http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode >= 500, fmt.Errorf("egress 回调返回 %d", resp.StatusCode)
	}
	return false, nil
}

// NoopEgress 丢弃出站事件（direct 模式，或 proxied 未配置回调时）。
type NoopEgress struct{}

// PushEvent 丢弃事件。
func (NoopEgress) PushEvent(string, any) error { return nil }

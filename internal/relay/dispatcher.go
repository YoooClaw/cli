package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Dispatcher 把 Relay 入站 frame loopback 到 daemon 自己的 HTTP/gateway 路由。
type Dispatcher struct {
	client      *Client
	httpBaseURL string
	httpToken   string
	clientLabel string
	logger      Logger
	// rpcClient 用于 gateway RPC 回环，带整体超时：本地 handler 一旦挂住，
	// 没超时的话 goroutine 永远不回收，手机端重试还会不断叠新的。
	rpcClient *http.Client
	// proxyClient 用于 HTTP 代理回环，只限响应头超时——SSE 等流式响应
	// 合法地长时间不结束，不能设整体超时。
	proxyClient *http.Client

	wsMu sync.Mutex
	ws   map[string]*websocket.Conn
}

// DispatcherOptions 是 Dispatcher 配置。
type DispatcherOptions struct {
	Client      *Client
	HTTPBaseURL string
	HTTPToken   string
	ClientLabel string
	Logger      Logger
}

// NewDispatcher 构造 frame dispatcher。
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	return &Dispatcher{
		client: opts.Client, httpBaseURL: strings.TrimRight(opts.HTTPBaseURL, "/"), httpToken: opts.HTTPToken,
		clientLabel: opts.ClientLabel, logger: opts.Logger, ws: map[string]*websocket.Conn{},
		rpcClient:   &http.Client{Timeout: 60 * time.Second},
		proxyClient: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 60 * time.Second}},
	}
}

// Start 绑定 client 入站 frame。
func (d *Dispatcher) Start() {
	d.client.OnInbound(func(frame Frame) {
		d.handleFrame(frame)
	})
}

// Cleanup 关闭所有本地 WS 子通道。
func (d *Dispatcher) Cleanup() {
	d.wsMu.Lock()
	defer d.wsMu.Unlock()
	for id, conn := range d.ws {
		d.logger.Info("Relay dispatcher: closing local WS id=" + id)
		_ = conn.Close()
	}
	d.ws = map[string]*websocket.Conn{}
}

// PushEvent daemon 主动推 Gateway event 给手机端。
func (d *Dispatcher) PushEvent(event string, payload any) {
	d.client.Send(EventFrame{Type: "event", Event: event, Payload: payload})
}

func (d *Dispatcher) handleFrame(frame Frame) {
	typ, _ := frame["type"].(string)
	id, _ := frame["id"].(string)
	d.logger.Debug(fmt.Sprintf("Relay dispatcher: handling frame type=%s id=%s", typ, firstNonEmpty(id, "N/A")))
	switch typ {
	case "req":
		go d.handleReq(frame)
	case "request":
		go d.handleRequest(frame)
	case "ws_open":
		go d.handleWSOpen(frame)
	case "ws_data":
		d.handleWSData(frame)
	case "ws_close":
		d.handleWSClose(frame)
	default:
		d.logger.Warn("Relay dispatcher: unknown frame type=" + typ)
	}
}

func (d *Dispatcher) handleReq(frame Frame) {
	id := stringField(frame, "id")
	method := stringField(frame, "method")
	params := frame["params"]
	if params == nil {
		params = map[string]any{}
	}
	d.logger.Info("Relay dispatcher: req id=" + id + " method=" + method)
	raw, status, err := d.loopbackJSON(http.MethodPost, "/gateway/"+url.PathEscape(method), params, nil)
	if err != nil {
		d.client.Send(RPCResponseFrame{Type: "res", ID: id, OK: false, Error: map[string]any{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}
	if status < 200 || status >= 300 {
		d.client.Send(RPCResponseFrame{Type: "res", ID: id, OK: false, Error: map[string]any{"code": "HTTP_ERROR", "message": fmt.Sprintf("daemon gateway returned %d: %s", status, preview(string(raw), 200))}})
		return
	}
	var body struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		d.client.Send(RPCResponseFrame{Type: "res", ID: id, OK: false, Error: map[string]any{"code": "INVALID_GATEWAY_RESPONSE", "message": err.Error()}})
		return
	}
	resp := RPCResponseFrame{Type: "res", ID: id, OK: body.OK}
	if body.OK {
		var payload any
		if len(body.Data) > 0 && string(body.Data) != "null" {
			_ = json.Unmarshal(body.Data, &payload)
		}
		resp.Payload = payload
	} else {
		var errPayload any
		if len(body.Error) > 0 {
			_ = json.Unmarshal(body.Error, &errPayload)
		}
		resp.Error = errPayload
	}
	d.client.Send(resp)
}

func (d *Dispatcher) handleRequest(frame Frame) {
	id := stringField(frame, "id")
	method := firstNonEmpty(stringField(frame, "method"), http.MethodGet)
	path := mapPath(stringField(frame, "path"))
	body := stringField(frame, "body")
	headers := headersFromFrame(frame["headers"])
	for k := range headers {
		lower := strings.ToLower(k)
		if lower == "authorization" || lower == "x-openclaw-password" {
			delete(headers, k)
		}
	}
	d.addInternalHeaders(headers)

	target := d.httpBaseURL + path
	d.logger.Info(fmt.Sprintf("Relay dispatcher: request id=%s %s %s -> %s", id, method, stringField(frame, "path"), path))
	req, err := http.NewRequest(method, target, requestBody(method, body))
	if err != nil {
		d.sendProxyError(id, 502, err.Error())
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := d.proxyClient.Do(req)
	if err != nil {
		d.sendProxyError(id, 502, "daemon loopback failed: "+err.Error())
		return
	}
	defer res.Body.Close()
	contentType := res.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		d.streamResponse(id, res.Body)
		return
	}
	raw, _ := io.ReadAll(res.Body)
	d.client.Send(ProxyResponseFrame{Type: "proxy_response", ID: id, Status: res.StatusCode, Headers: flattenHeaders(res.Header), Body: string(raw)})
}

func (d *Dispatcher) streamResponse(id string, body io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			d.client.Send(StreamFrame{Type: "stream", ID: id, State: "delta", Data: string(buf[:n])})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			d.sendProxyError(id, 502, "stream error: "+err.Error())
			return
		}
	}
	d.client.Send(StreamFrame{Type: "stream", ID: id, State: "end", Data: ""})
}

func (d *Dispatcher) handleWSOpen(frame Frame) {
	id := stringField(frame, "id")
	path := mapPath(stringField(frame, "path"))
	wsURL := "ws" + strings.TrimPrefix(d.httpBaseURL, "http") + path
	headers := http.Header{}
	for k, v := range headersFromFrame(frame["headers"]) {
		headers.Set(k, v)
	}
	d.addInternalHeadersMap(headers)
	d.logger.Info(fmt.Sprintf("Relay dispatcher: ws_open id=%s path=%s -> %s", id, path, wsURL))
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		d.client.Send(WSCloseFrame{Type: "ws_close", ID: id, Code: 1011, Reason: "failed to connect: " + err.Error()})
		return
	}
	d.wsMu.Lock()
	if old := d.ws[id]; old != nil {
		_ = old.Close()
	}
	d.ws[id] = conn
	d.wsMu.Unlock()
	d.client.Send(WSOpenedFrame{Type: "ws_opened", ID: id})
	go d.readLocalWS(id, conn)
}

func (d *Dispatcher) readLocalWS(id string, conn *websocket.Conn) {
	defer func() {
		d.wsMu.Lock()
		if d.ws[id] == conn {
			delete(d.ws, id)
		}
		d.wsMu.Unlock()
		_ = conn.Close()
	}()
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			d.client.Send(WSCloseFrame{Type: "ws_close", ID: id, Code: 1006, Reason: err.Error()})
			return
		}
		if msgType == websocket.TextMessage || msgType == websocket.BinaryMessage {
			d.client.Send(WSDataFrame{Type: "ws_data", ID: id, Data: string(data)})
		}
	}
}

func (d *Dispatcher) handleWSData(frame Frame) {
	id := stringField(frame, "id")
	data := stringField(frame, "data")
	d.wsMu.Lock()
	conn := d.ws[id]
	d.wsMu.Unlock()
	if conn == nil {
		d.logger.Warn("Relay dispatcher: ws_data dropped, connection not found id=" + id)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
		d.logger.Warn("Relay dispatcher: ws_data write failed id=" + id + ": " + err.Error())
		_ = conn.Close()
	}
}

func (d *Dispatcher) handleWSClose(frame Frame) {
	id := stringField(frame, "id")
	d.wsMu.Lock()
	conn := d.ws[id]
	delete(d.ws, id)
	d.wsMu.Unlock()
	if conn != nil {
		code := intField(frame, "code", websocket.CloseNormalClosure)
		reason := stringField(frame, "reason")
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		_ = conn.Close()
	}
}

func (d *Dispatcher) loopbackJSON(method, path string, body any, extra map[string]string) ([]byte, int, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest(method, d.httpBaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	headers := map[string]string{}
	for k, v := range extra {
		headers[k] = v
	}
	d.addInternalHeaders(headers)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := d.rpcClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	return raw, res.StatusCode, err
}

func (d *Dispatcher) addInternalHeaders(headers map[string]string) {
	headers[InternalHTTPHeader] = "1"
	headers[InternalClientLabelHeader] = d.clientLabel
	if d.httpToken != "" {
		headers["Authorization"] = "Bearer " + d.httpToken
	}
}

func (d *Dispatcher) addInternalHeadersMap(headers http.Header) {
	headers.Set(InternalHTTPHeader, "1")
	headers.Set(InternalClientLabelHeader, d.clientLabel)
	if d.httpToken != "" {
		headers.Set("Authorization", "Bearer "+d.httpToken)
	}
}

func (d *Dispatcher) sendProxyError(id string, status int, message string) {
	d.client.Send(ProxyErrorFrame{Type: "proxy_error", ID: id, Status: status, Message: message})
}

func mapPath(p string) string {
	if p == "/api/message/messageBridge/send" {
		return "/notifications"
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func requestBody(method, body string) io.Reader {
	if method == http.MethodGet || method == http.MethodHead {
		return nil
	}
	return strings.NewReader(body)
}

func headersFromFrame(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, raw := range m {
			if s, ok := raw.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func flattenHeaders(headers http.Header) map[string]string {
	out := map[string]string{}
	for k, values := range headers {
		out[strings.ToLower(k)] = strings.Join(values, ", ")
	}
	return out
}

func stringField(m Frame, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func intField(m Frame, key string, fallback int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

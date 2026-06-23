package yclib

import (
	"encoding/json"

	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
)

// daemonClient 用解析出的 gateway token 构造 daemon RPC client（不读隐式凭证，
// token 解析逻辑见 Client.gatewayToken）。
func (c *Client) daemonClient() *daemon.Client {
	return daemon.NewClientWithToken(c.paths, c.gatewayToken())
}

// assertDaemon 确保 daemon 在运行，否则返回 CodeDaemonNotRunning。
// arc §5：library 不隐式 fork daemon —— 未运行时报错，由调用方决定是否 Daemon().Start()。
func (c *Client) assertDaemon() error {
	return daemon.AssertRunning(c.paths)
}

// daemonRequest 发起一次 daemon RPC：先确保运行 → 调用 → 把响应体解码进 out（typed），
// 非 2xx 统一转成结构化 *yclib.Error。out 可为 nil（不关心响应体）。
func (c *Client) daemonRequest(method, path string, body, out any) error {
	if err := c.assertDaemon(); err != nil {
		return err
	}
	status, resBody, err := c.daemonClient().Request(method, path, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return errorFromEnvelope(status, resBody)
	}
	if out != nil {
		return decodeInto(resBody, out)
	}
	return nil
}

// gatewayRequest 处理 gateway 风格端点（响应封装 {ok, data, error}）：
// 先确保 daemon 运行 → 调用 → ok:false 或非 2xx 转 *yclib.Error；否则把 data 解进 dataOut。
func (c *Client) gatewayRequest(method, path string, body, dataOut any) error {
	if err := c.assertDaemon(); err != nil {
		return err
	}
	status, resBody, err := c.daemonClient().Request(method, path, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return errorFromEnvelope(status, resBody)
	}
	if m, ok := resBody.(map[string]any); ok {
		if okVal, exists := m["ok"]; exists && okVal == false {
			return errorFromEnvelope(status, m)
		}
		if dataOut != nil {
			if data, has := m["data"]; has {
				return decodeInto(data, dataOut)
			}
		}
	}
	if dataOut != nil {
		return decodeInto(resBody, dataOut)
	}
	return nil
}

// decodeInto 把 daemon 返回的无类型 JSON 值（any）重编码后解进具体 struct。
func decodeInto(v, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return errs.New(errs.CodeUnknown, "daemon 响应序列化失败："+err.Error())
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errs.New(errs.CodeUnknown, "daemon 响应解码失败："+err.Error())
	}
	return nil
}

// errorFromEnvelope 从 daemon 错误体 {ok:false,error:{code,message}} 提取结构化错误；
// 提取不到则回落到带状态码的通用错误。
func errorFromEnvelope(status int, body any) error {
	if m, ok := body.(map[string]any); ok {
		if e, ok := m["error"].(map[string]any); ok {
			code, _ := e["code"].(string)
			msg, _ := e["message"].(string)
			if code == "" {
				code = errs.CodeUnknown
			}
			if msg == "" {
				msg = "daemon 返回错误"
			}
			return errs.New(code, msg, map[string]any{"status": status})
		}
	}
	return errs.New(errs.CodeUnknown, "daemon 返回非 2xx", map[string]any{"status": status})
}

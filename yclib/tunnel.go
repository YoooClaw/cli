package yclib

import (
	"context"
	"net/url"
)

// TunnelClient 暴露 Relay 隧道运维（经 daemon 的 /tunnel/* 端点代理）。
// daemon 未运行时返回 CodeDaemonNotRunning（不自动拉起，见 arc §5）。
type TunnelClient struct {
	c *Client
}

// Tunnel 返回 Relay 隧道子 client。
func (c *Client) Tunnel() *TunnelClient { return &TunnelClient{c: c} }

// TunnelStatusResult 是 Relay 隧道整体状态。
type TunnelStatusResult struct {
	Mode                 string       `json:"mode"`
	CredentialMode       string       `json:"credentialMode"`
	DefaultLabel         string       `json:"defaultLabel"`
	Connected            bool         `json:"connected"`
	RelayURL             string       `json:"relayUrl"`
	Enabled              bool         `json:"enabled"`
	ReconnectAttempt     int          `json:"reconnectAttempt"`
	LastDisconnectReason string       `json:"lastDisconnectReason"`
	Note                 string       `json:"note"`
	Tunnels              []TunnelInfo `json:"tunnels"`
}

// Status 查询隧道状态。client 为空查全部，非空只看指定 clientLabel。
// 等价于 CLI `tunnel status`。
func (t *TunnelClient) Status(ctx context.Context, client string) (TunnelStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return TunnelStatusResult{}, err
	}
	path := "/tunnel/status"
	if client != "" {
		path += "?client=" + url.QueryEscape(client)
	}
	var out TunnelStatusResult
	if err := t.c.daemonRequest("GET", path, nil, &out); err != nil {
		return TunnelStatusResult{}, err
	}
	return out, nil
}

// TunnelReconnectResult 是强制重连的结果。
type TunnelReconnectResult struct {
	Reconnected bool     `json:"reconnected"`
	Mode        string   `json:"mode"`
	Labels      []string `json:"labels"`
	Note        string   `json:"note"`
}

// Reconnect 强制重连隧道。client 为空重连全部，非空只重连指定 clientLabel。
// 等价于 CLI `tunnel reconnect`。
func (t *TunnelClient) Reconnect(ctx context.Context, client string) (TunnelReconnectResult, error) {
	if err := ctx.Err(); err != nil {
		return TunnelReconnectResult{}, err
	}
	var body any
	if client != "" {
		body = map[string]any{"client": client}
	}
	var out TunnelReconnectResult
	if err := t.c.daemonRequest("POST", "/tunnel/reconnect", body, &out); err != nil {
		return TunnelReconnectResult{}, err
	}
	return out, nil
}

// TunnelTestResult 是 ingest + 鉴权 echo 回环自检结果。
type TunnelTestResult struct {
	OK          bool   `json:"ok"`
	Mode        string `json:"mode"`
	ClientLabel string `json:"clientLabel"`
	Loopback    struct {
		OK bool `json:"ok"`
	} `json:"loopback"`
}

// Test 触发 daemon 本地 ingest + 鉴权 echo 回环自检。等价于 CLI `tunnel +test`。
func (t *TunnelClient) Test(ctx context.Context, client string) (TunnelTestResult, error) {
	if err := ctx.Err(); err != nil {
		return TunnelTestResult{}, err
	}
	var body any
	if client != "" {
		body = map[string]any{"client": client}
	}
	var out TunnelTestResult
	if err := t.c.daemonRequest("POST", "/tunnel/test", body, &out); err != nil {
		return TunnelTestResult{}, err
	}
	return out, nil
}

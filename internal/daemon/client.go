package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// Client 是 CLI ↔ daemon 的内部 HTTP RPC（localhost）。
type Client struct {
	BaseURL string
	token   string
	timeout time.Duration
}

// NewClient 按 lock / config 推导 base URL 与 gateway token。
func NewClient(p paths.Paths) *Client {
	cfg, _ := config.Load(p)
	state := State(p)
	bind := cfg.Daemon.Bind
	port := cfg.Daemon.Port
	if state.Lock != nil {
		bind = state.Lock.Bind
		port = state.Lock.Port
	}
	host := bind
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	tokenRef, _ := creds.ResolveGatewayToken(cfg)
	return &Client{
		BaseURL: "http://" + host + ":" + strconv.Itoa(port),
		token:   tokenRef.Value,
		timeout: 10 * time.Second,
	}
}

// Request 调 daemon；连接被拒/超时统一报 DAEMON_NOT_RUNNING。
func (c *Client) Request(method, path string, body any) (int, any, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return 0, nil, errs.New(errs.CodeNetworkError, "构造请求失败")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") || ctx.Err() != nil {
			return 0, nil, errs.New(errs.CodeDaemonNotRunning, "daemon 未启动或无响应",
				map[string]any{"hint": "先执行 yoooclaw daemon start", "baseUrl": c.BaseURL})
		}
		return 0, nil, errs.New(errs.CodeNetworkError, "调用 daemon 失败："+err.Error())
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	var parsed any
	if len(text) > 0 {
		if json.Unmarshal(text, &parsed) != nil {
			parsed = string(text)
		}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return resp.StatusCode, parsed, errs.New(errs.CodeUnauthorized, "daemon 拒绝鉴权（token 不一致）",
			map[string]any{"status": resp.StatusCode})
	}
	return resp.StatusCode, parsed, nil
}

// AssertRunning 确保 daemon 在运行，否则报 DAEMON_NOT_RUNNING（🟡 命令前置检查）。
func AssertRunning(p paths.Paths) error {
	state := State(p)
	if !state.Running {
		return errs.New(errs.CodeDaemonNotRunning, "daemon 未运行",
			map[string]any{"hint": "先执行 yoooclaw daemon start", "stale": state.Stale})
	}
	return nil
}

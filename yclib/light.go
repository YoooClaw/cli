package yclib

import (
	"context"

	"github.com/YoooClaw/cli/internal/light"
)

// LightClient 暴露灯效硬件控制能力。经 Client.Light() 获取。
//
// 注意：灯效下发**经 daemon 代理**（daemon 持有灯效云 API key 并实际发送），属 daemon
// 依赖能力 —— daemon 未运行时返回 CodeDaemonNotRunning（不自动拉起，见 arc §5）。
type LightClient struct {
	c *Client
}

// Light 返回灯效子 client。
func (c *Client) Light() *LightClient { return &LightClient{c: c} }

// LightSendResult 是灯效下发结果（re-export 自底层，typed DTO）。
type LightSendResult = light.SendResult

// LightSendOpts 是 Send 的入参：Segments / Preset / Rule 三选一。
//   - Segments：直接给出灯效段（结构见灯效协议），服务端校验。
//   - Preset：内置预设 id（如 "red-strobe-3"）。
//   - Rule：已保存的灯效规则 id。
//
// Repeat/RepeatTimes 控制重复；Reason/Title 为可选元信息（透传到下发记录）。
type LightSendOpts struct {
	Segments    []map[string]any
	Preset      string
	Rule        string
	Repeat      bool
	RepeatTimes *float64
	Reason      string
	Title       string
}

// Send 下发灯效到硬件。等价于 CLI `light send`，返回 typed 结果。
// Segments/Preset/Rule 必须恰好提供一个；否则返回 CodeInvalidArgument。
func (l *LightClient) Send(ctx context.Context, opts LightSendOpts) (LightSendResult, error) {
	if err := ctx.Err(); err != nil {
		return LightSendResult{}, err
	}
	n := 0
	body := map[string]any{}
	if len(opts.Segments) > 0 {
		body["segments"] = opts.Segments
		n++
	}
	if opts.Preset != "" {
		body["preset"] = opts.Preset
		n++
	}
	if opts.Rule != "" {
		body["rule"] = opts.Rule
		n++
	}
	if n != 1 {
		return LightSendResult{}, newInvalidArg("需要 Segments / Preset / Rule 之一（且只能一个）")
	}
	if opts.Repeat {
		body["repeat"] = true
	}
	if opts.RepeatTimes != nil {
		body["repeat_times"] = *opts.RepeatTimes
	}
	if opts.Reason != "" {
		body["reason"] = opts.Reason
	}
	if opts.Title != "" {
		body["title"] = opts.Title
	}
	var res LightSendResult
	if err := l.c.daemonRequest("POST", "/light/send", body, &res); err != nil {
		return LightSendResult{}, err
	}
	l.c.logf("yclib light.send ok=%v status=%d", res.OK, res.Status)
	return res, nil
}

// Blink 发送内置连通性测试灯效（red-strobe-3）。等价于 CLI `light +blink`。
func (l *LightClient) Blink(ctx context.Context) (LightSendResult, error) {
	return l.Send(ctx, LightSendOpts{Preset: "red-strobe-3"})
}

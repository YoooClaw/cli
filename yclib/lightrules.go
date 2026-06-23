package yclib

import (
	"context"
)

// LightRulesClient 暴露灯效规则管理（经 daemon 的 gateway/lightrules.* 端点代理）。
// daemon 未运行时返回 CodeDaemonNotRunning（不自动拉起，见 arc §5）。
type LightRulesClient struct {
	c *Client
}

// LightRules 返回灯效规则子 client。
func (c *Client) LightRules() *LightRulesClient { return &LightRulesClient{c: c} }

// lightrulesListEnvelope 对应 gateway lightrules.list 的 data 部分。
type lightrulesListEnvelope struct {
	Rules []LightRule `json:"rules"`
}

// List 列出全部灯效规则。等价于 CLI `lightrule list`。
func (lr *LightRulesClient) List(ctx context.Context) ([]LightRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var data lightrulesListEnvelope
	if err := lr.c.gatewayRequest("POST", "/gateway/lightrules.list", map[string]any{}, &data); err != nil {
		return nil, err
	}
	if data.Rules == nil {
		data.Rules = []LightRule{}
	}
	return data.Rules, nil
}

// Get 按 name 取单条规则；不存在返回 CodeNotFound。
func (lr *LightRulesClient) Get(ctx context.Context, name string) (LightRule, error) {
	rules, err := lr.List(ctx)
	if err != nil {
		return LightRule{}, err
	}
	for _, r := range rules {
		if r.Name == name {
			return r, nil
		}
	}
	return LightRule{}, newNotFound("规则不存在：" + name)
}

// Create 创建规则。params 为规则字段（name / description / segments / matchRules 等，
// 与 CLI `lightrule create --from-file` 的 JSON 同构）。
func (lr *LightRulesClient) Create(ctx context.Context, params map[string]any) (LightRule, error) {
	if err := ctx.Err(); err != nil {
		return LightRule{}, err
	}
	var out LightRule
	if err := lr.c.gatewayRequest("POST", "/gateway/lightrules.create", params, &out); err != nil {
		return LightRule{}, err
	}
	return out, nil
}

// Update 更新规则（按 name）。params 为要变更的字段。
func (lr *LightRulesClient) Update(ctx context.Context, name string, params map[string]any) (LightRule, error) {
	if err := ctx.Err(); err != nil {
		return LightRule{}, err
	}
	if params == nil {
		params = map[string]any{}
	}
	params["name"] = name
	var out LightRule
	if err := lr.c.gatewayRequest("POST", "/gateway/lightrules.update", params, &out); err != nil {
		return LightRule{}, err
	}
	return out, nil
}

// Delete 删除规则（按 name）。
func (lr *LightRulesClient) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return lr.c.gatewayRequest("POST", "/gateway/lightrules.delete", map[string]any{"name": name}, nil)
}

// SetEnabled 启用/停用单条规则（按 name）。
func (lr *LightRulesClient) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return lr.c.gatewayRequest("POST", "/gateway/lightrules.update", map[string]any{"name": name, "enabled": enabled}, nil)
}

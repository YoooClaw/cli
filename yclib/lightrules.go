package yclib

import (
	"context"
	"strings"

	"github.com/YoooClaw/cli/internal/errs"
	internalrules "github.com/YoooClaw/cli/internal/lightrule"
)

const defaultLightRulesURL = "https://openclaw-service.yoooclaw.com/api/plugin/notification-intelligence/light-rules"

// LightRulesClient 直接调用 Notification Intelligence Service 的云端规则 API。
type LightRulesClient struct{ c *Client }

// LightRules 返回灯效规则子 client。
func (c *Client) LightRules() *LightRulesClient { return &LightRulesClient{c: c} }

func (lr *LightRulesClient) cloudClient() (*internalrules.CloudClient, error) {
	if strings.TrimSpace(lr.c.apiKey) == "" {
		return nil, errs.New(errs.CodeCredentialMissing, "yclib Config.APIKey 未设置")
	}
	baseURL := lr.c.lightRulesURL
	if baseURL == "" {
		baseURL = defaultLightRulesURL
	}
	return &internalrules.CloudClient{APIKey: lr.c.apiKey, BaseURL: baseURL}, nil
}

// List 列出云端全部灯效规则。等价于 CLI `lightrule list`。
func (lr *LightRulesClient) List(ctx context.Context) ([]LightRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := lr.cloudClient()
	if err != nil {
		return nil, err
	}
	raw, err := client.List()
	if err != nil {
		return nil, cloudRuleLibraryError(err)
	}
	rules := make([]LightRule, 0, len(raw))
	for _, item := range raw {
		var rule LightRule
		if err := decodeInto(item, &rule); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Get 按 name 取单条云端规则；不存在返回 CodeNotFound。
func (lr *LightRulesClient) Get(ctx context.Context, name string) (LightRule, error) {
	rules, err := lr.List(ctx)
	if err != nil {
		return LightRule{}, err
	}
	for _, rule := range rules {
		if rule.Name == name {
			return rule, nil
		}
	}
	return LightRule{}, newNotFound("规则不存在：" + name)
}

// Create 使用 params.ruleText 让云端独立 Agent 编译并创建规则。
func (lr *LightRulesClient) Create(ctx context.Context, params map[string]any) (LightRule, error) {
	if err := ctx.Err(); err != nil {
		return LightRule{}, err
	}
	ruleText, _ := params["ruleText"].(string)
	client, err := lr.cloudClient()
	if err != nil {
		return LightRule{}, err
	}
	result, err := client.Create(ruleText)
	if err != nil {
		return LightRule{}, cloudRuleLibraryError(err)
	}
	var rule LightRule
	if nested, ok := result["rule"]; ok {
		_ = decodeInto(nested, &rule)
	} else {
		_ = decodeInto(result, &rule)
	}
	return rule, nil
}

// Update 按云端 id/name 局部更新规则。
func (lr *LightRulesClient) Update(ctx context.Context, identifier string, params map[string]any) (LightRule, error) {
	if err := ctx.Err(); err != nil {
		return LightRule{}, err
	}
	client, err := lr.cloudClient()
	if err != nil {
		return LightRule{}, err
	}
	result, err := client.Update(identifier, params)
	if err != nil {
		return LightRule{}, cloudRuleLibraryError(err)
	}
	var rule LightRule
	if nested, ok := result["rule"]; ok {
		_ = decodeInto(nested, &rule)
	} else {
		_ = decodeInto(result, &rule)
	}
	return rule, nil
}

// Delete 按云端 id/name 删除规则。
func (lr *LightRulesClient) Delete(ctx context.Context, identifier string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := lr.cloudClient()
	if err != nil {
		return err
	}
	_, err = client.Delete(identifier)
	return cloudRuleLibraryError(err)
}

// SetEnabled 启用/停用单条云端规则。
func (lr *LightRulesClient) SetEnabled(ctx context.Context, identifier string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := lr.cloudClient()
	if err != nil {
		return err
	}
	_, err = client.Update(identifier, map[string]any{"enabled": enabled})
	return cloudRuleLibraryError(err)
}

func cloudRuleLibraryError(err error) error {
	if err == nil {
		return nil
	}
	remote, ok := err.(*internalrules.RemoteError)
	if !ok {
		return err
	}
	switch remote.Status {
	case 401, 403:
		return errs.New(errs.CodeUnauthorized, remote.Message)
	case 404:
		return errs.New(errs.CodeNotFound, remote.Message)
	default:
		return errs.New(remote.Code, remote.Message, map[string]any{"status": remote.Status})
	}
}

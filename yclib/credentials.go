package yclib

import (
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
)

// CredentialSource 是 library 的显式凭证注入点。daemon 依赖能力需要 gateway token
// 才能向本地 daemon 鉴权；library 不隐式读 keychain/config，除非 Config.AllowImplicitCredentials。
type CredentialSource interface {
	// GatewayToken 返回连接本地 daemon 的 token；返回空串表示“未提供”。
	GatewayToken() string
}

// StaticCredentials 是最简单的显式凭证实现：直接持有 token 字面量。
type StaticCredentials struct {
	Token string
}

// GatewayToken 实现 CredentialSource。
func (s StaticCredentials) GatewayToken() string { return s.Token }

// gatewayToken 解析当前 Client 应使用的 daemon token：
//  1. 显式 Config.Credentials 优先；
//  2. 否则，仅当 Config.AllowImplicitCredentials 为真时，回退到 profile 的 config/keychain；
//  3. 否则返回空串（daemon 调用将因缺鉴权被拒，报 CodeUnauthorized）。
func (c *Client) gatewayToken() string {
	if c.creds != nil {
		if t := c.creds.GatewayToken(); t != "" {
			return t
		}
	}
	if c.allowImplicitCreds {
		cfg, _ := config.Load(c.paths)
		if ref, err := creds.ResolveGatewayToken(cfg); err == nil {
			return ref.Value
		}
	}
	return ""
}

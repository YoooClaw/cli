// Package yclib 是 yc-cli 的进程内公开 API 门面（路径 C / library 化）。
//
// 设计约束见 software 仓库 architecture/arc-cli-library-integration.md：
//   - 唯一对外入口；internal/ 实现保持私有（本包位于 module 根下，故可合法 import internal）。
//   - 显式 Config 注入，不读隐式 env / active-profile 文件 / 全局 flag。
//   - 不 os.Exit、不写 os.Stdout/Stderr；以返回值 + error 表达结果。
//   - 公开方法首参 context.Context；返回具体导出 struct，不返回 any。
//
// 本文件提供入口 New(Config) 与 Client 装配。能力按子 client 分组（见各 *.go）。
package yclib

import (
	"errors"
	"strings"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// Logger 是可选的 library 日志出口。library 自身**不**向 stdout/stderr 打印，
// 调用方若想观测内部进度可注入实现；nil 表示静默。
type Logger interface {
	Logf(format string, args ...any)
}

// Config 是 library 的唯一配置注入点。
//
// 缺省值产出可预测行为：Profile 空 → "default"；RootDir 空 → ~/.yoooclaw（不读
// YOOOCLAW_HOME 等环境变量）。library 路径严禁隐式读 env / 文件做行为决策。
type Config struct {
	// Profile 显式 profile 名。空 → "default"。不回退到 $YOOOCLAW_PROFILE 或 active-profile 文件。
	Profile string
	// RootDir 覆盖数据根目录（多租户 / 测试隔离）。空 → paths.DefaultRoot()（~/.yoooclaw）。
	RootDir string
	// Logger 可选；nil 表示静默。
	Logger Logger
	// APIKey 是 Notification Intelligence 等云端插件 API 使用的账号 key。
	// 默认空且不做隐式解析，调用云端能力时返回 CodeCredentialMissing。
	APIKey string
	// NotificationIntelligenceLightRulesURL 可选覆盖云端灯效规则 API。
	// 空值使用 production 插件侧默认入口。
	NotificationIntelligenceLightRulesURL string

	// Credentials 显式注入 daemon 依赖能力所需的凭证（如 gateway token）。
	// nil 表示不提供；是否回退到隐式来源由 AllowImplicitCredentials 决定。
	Credentials CredentialSource

	// AllowImplicitCredentials 为真时，在 Credentials 未提供 token 的情况下，
	// 允许回退到当前 profile 的 config/keychain 解析（迁移便利的 opt-in 开关）。
	// 默认 false：严格显式，符合 arc §4「不隐式读凭证」。
	AllowImplicitCredentials bool
}

// Client 是 library 的根句柄，按能力分组暴露子 client。构造无进程级副作用
// （不建目录、不写文件、不读环境变量）。Client 可并发只读使用。
type Client struct {
	profile            string
	rootDir            string
	paths              paths.Paths
	logger             Logger
	creds              CredentialSource
	allowImplicitCreds bool
	apiKey             string
	lightRulesURL      string
}

// New 按显式 Config 装配 Client。不产生任何 IO 副作用。
func New(cfg Config) (*Client, error) {
	profile := strings.TrimSpace(cfg.Profile)
	if profile == "" {
		profile = paths.DefaultProfile
	}
	root := strings.TrimSpace(cfg.RootDir)
	if root == "" {
		root = paths.DefaultRoot()
	}
	return &Client{
		profile:            profile,
		rootDir:            root,
		paths:              paths.ForRoot(root, profile),
		logger:             cfg.Logger,
		creds:              cfg.Credentials,
		allowImplicitCreds: cfg.AllowImplicitCredentials,
		apiKey:             strings.TrimSpace(cfg.APIKey),
		lightRulesURL:      strings.TrimSpace(cfg.NotificationIntelligenceLightRulesURL),
	}, nil
}

// Profile 返回当前 Client 绑定的 profile 名。
func (c *Client) Profile() string { return c.profile }

// logf 经可选 Logger 输出；未注入时静默。
func (c *Client) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Logf(format, args...)
	}
}

// Error 是 library 对外暴露的结构化错误类型（与内部错误同源）。
// 调用方可用 errors.As(err, &yclib.Error{}) 判别，或用 ErrorCode(err) 取错误码。
type Error = errs.Error

// 常用错误码（与 internal/errs 同值，进入对外半正式契约）。
const (
	CodeUnknown            = errs.CodeUnknown
	CodeInvalidArgument    = errs.CodeInvalidArgument
	CodeNotFound           = errs.CodeNotFound
	CodeStorageUnavailable = errs.CodeStorageUnavailable
	CodeDaemonNotRunning   = errs.CodeDaemonNotRunning
	CodeUnauthorized       = errs.CodeUnauthorized
	CodeNetworkError       = errs.CodeNetworkError
	CodeCredentialMissing  = errs.CodeCredentialMissing
)

// ErrorCode 提取错误码；非结构化错误返回空串。便于调用方做类型安全分支，
// 而非解析错误字符串。
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Package config 定义 daemon 配置 schema 并提供读写 + 点号路径 get/set/unset + 遮罩
// （对齐 TS 版 src/config/schema.ts 与 src/config/store.ts）。
//
// 敏感字段（gateway token / webhook secret）不直接落这里，而用 *Ref 抽象引用指向
// credentials 文件 / keychain / env。
package config

import (
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

// ConfigVersion 是当前 config.json schema 版本。
const ConfigVersion = 1

// 默认值常量。
const (
	DefaultPort         = 18789
	DefaultBind         = "127.0.0.1"
	DefaultImageMaxByte = 20 * 1024 * 1024
)

// RelayHost 是默认 Relay 主机；可由构建期 ldflags 注入覆盖，未注入时回退生产域名。
var RelayHost = "openclaw-service.yoooclaw.com"

// DefaultRelayURL 返回默认 Relay 隧道地址（复用 phone-notifications 托管 Relay）。
func DefaultRelayURL() string {
	return "wss://" + RelayHost + "/message/messages/ws/plugin"
}

// SecretRefPaths 是 config.json 里需要遮罩展示的敏感引用字段（点号路径）。
var SecretRefPaths = []string{
	"auth.tokenRef",
	"lightRules.evaluator.webhookSecretRef",
}

// DaemonSection 守护进程监听配置。
type DaemonSection struct {
	Bind     string `json:"bind"`
	Port     int    `json:"port"`
	LogLevel string `json:"logLevel"`
	Detach   bool   `json:"detach"`
}

// AuthSection 鉴权配置。
type AuthSection struct {
	Mode     string `json:"mode"`
	TokenRef string `json:"tokenRef"`
}

// RelaySection Relay 隧道配置。
type RelaySection struct {
	URL                string `json:"url"`
	HeartbeatSec       int    `json:"heartbeatSec"`
	ReconnectBackoffMs int    `json:"reconnectBackoffMs"`
	Enabled            bool   `json:"enabled"`
}

// NotificationSection 通知存储配置。RetentionDays 为 nil 表示永久保存。
type NotificationSection struct {
	RetentionDays *int     `json:"retentionDays"`
	IgnoredApps   []string `json:"ignoredApps"`
}

// WebhookEvaluatorSection 灯效 webhook 评估器配置。
type WebhookEvaluatorSection struct {
	Mode             string `json:"mode"`
	WebhookURL       string `json:"webhookUrl"`
	WebhookSecretRef string `json:"webhookSecretRef"`
	TimeoutMs        int    `json:"timeoutMs"`
	Retries          int    `json:"retries"`
}

// LightRulesSection 灯效规则配置。
type LightRulesSection struct {
	Enabled   bool                     `json:"enabled"`
	Evaluator *WebhookEvaluatorSection `json:"evaluator,omitempty"`
}

// AutoUpdateSection 自动更新配置。
type AutoUpdateSection struct {
	Enabled bool   `json:"enabled"`
	Channel string `json:"channel"`
}

// OutputSection 输出默认格式配置。
type OutputSection struct {
	DefaultFormat string `json:"defaultFormat"`
}

// ImageSection 图片下载配置。
type ImageSection struct {
	MaxBytes int64 `json:"maxBytes"`
}

// Config 是 per-profile config.json 的完整结构。
type Config struct {
	Version      int                 `json:"version"`
	Daemon       DaemonSection       `json:"daemon"`
	Auth         AuthSection         `json:"auth"`
	Relay        RelaySection        `json:"relay"`
	Notification NotificationSection `json:"notification"`
	LightRules   LightRulesSection   `json:"lightRules"`
	AutoUpdate   AutoUpdateSection   `json:"autoUpdate"`
	Output       OutputSection       `json:"output"`
	Image        ImageSection        `json:"image"`
}

// Default 生成某个 profile 的默认 config。tokenRef/secretRef 指向该 profile 的 credentials 文件。
func Default(credentialsPath string) Config {
	return Config{
		Version: ConfigVersion,
		Daemon: DaemonSection{
			Bind:     DefaultBind,
			Port:     DefaultPort,
			LogLevel: "info",
			Detach:   true,
		},
		Auth: AuthSection{
			Mode:     "token",
			TokenRef: "file:" + credentialsPath + "#gatewayToken",
		},
		Relay: RelaySection{
			URL:                DefaultRelayURL(),
			HeartbeatSec:       10,
			ReconnectBackoffMs: 2000,
			Enabled:            true,
		},
		Notification: NotificationSection{
			RetentionDays: nil,
			IgnoredApps:   []string{},
		},
		LightRules: LightRulesSection{Enabled: true},
		AutoUpdate: AutoUpdateSection{Enabled: true, Channel: "stable"},
		Output:     OutputSection{DefaultFormat: "auto"},
		Image:      ImageSection{MaxBytes: DefaultImageMaxByte},
	}
}

// DefaultEvaluator 返回默认 webhook 评估器（config init 启用灯效评估时填充）。
func DefaultEvaluator(credentialsPath string) WebhookEvaluatorSection {
	return WebhookEvaluatorSection{
		Mode:             "webhook",
		WebhookURL:       "",
		WebhookSecretRef: "file:" + credentialsPath + "#evaluatorSecret",
		TimeoutMs:        5000,
		Retries:          1,
	}
}

// Exists 报告 config.json 是否已存在。
func Exists(p paths.Paths) bool {
	return fsutil.Exists(p.Config)
}

// Load 读取 config；缺失字段用默认值补齐（Go json.Unmarshal 覆盖默认结构体 = deepMerge 语义）。
// 文件不存在则返回纯默认。
func Load(p paths.Paths) (Config, error) {
	cfg := Default(p.Credentials)
	if _, err := fsutil.ReadJSON(p.Config, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Require 要求 config 必须已存在（如 daemon 启动），否则报错。
func Require(p paths.Paths) (Config, error) {
	if !Exists(p) {
		return Config{}, errs.New(errs.CodeConfigInvalid,
			"profile `"+p.Profile+"` 尚未初始化",
			map[string]any{"hint": "先运行 yoooclaw config init", "checkedPaths": []string{p.Config}})
	}
	return Load(p)
}

// Save 写 config.json（0644）。
func Save(p paths.Paths, cfg Config) error {
	return fsutil.WriteJSON(p.Config, cfg, fsutil.ConfigFileMode)
}

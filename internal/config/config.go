// Package config 定义 daemon 配置 schema 并提供读写 + 点号路径 get/set/unset + 遮罩。
//
// 敏感字段（gateway token / webhook secret）不直接落这里，而用 *Ref 抽象引用指向
// credentials 文件 / keychain / env。
package config

import (
	"strings"

	"github.com/YoooClaw/cli/internal/envhost"
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

// RelayPath 是 Relay 隧道 WebSocket 的固定路径。
const RelayPath = "/message/messages/ws/plugin"

// DefaultRelayURL 返回默认 Relay 隧道地址；主机随 PHONE_NOTIFICATIONS_ENV 切换
// （development/test/production，见 internal/envhost），复用 phone-notifications 托管 Relay。
func DefaultRelayURL() string {
	return "wss://" + envhost.Host() + RelayPath
}

// ResolveRelayURL 返回 daemon 实际应连接的 Relay 地址。
// 若配置里持久化的仍是某个环境的内置默认主机（而非用户自定义 URL），则按当前
// PHONE_NOTIFICATIONS_ENV 重新解析，使已初始化的 profile 也能跟随环境切换；
// 用户显式改过的自定义 URL 始终保留。
func ResolveRelayURL(cfg Config) string {
	url := cfg.Relay.URL
	if url == "" || envhost.IsDefaultHost(stripRelayURL(url)) {
		return DefaultRelayURL()
	}
	return url
}

// stripRelayURL 从 Relay URL 中提取主机部分（去掉 scheme 与已知路径）供默认值判定。
func stripRelayURL(url string) string {
	host := envhost.Normalize(url)
	return strings.TrimSuffix(host, RelayPath)
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

// Ingress 运行模式（见 docs/design/ingress-layering.md）。
const (
	// IngressStandalone：daemon 自连 Relay 隧道接收数据（默认）。
	IngressStandalone = "standalone"
	// IngressProxied：宿主（如 hermes-plugin）代理到手机的连接，daemon 关闭隧道、
	// 仅暴露 ingest API 收数据，出站事件回投宿主 egress 回调。
	IngressProxied = "proxied"
	// IngressDirect：不连隧道、不回投，仅供 LAN / 测试直接 POST。
	IngressDirect = "direct"
)

// IngressSection 选择数据入站的传输 owner（L3 传输层）。
type IngressSection struct {
	Mode           string                `json:"mode"`
	EgressCallback EgressCallbackSection `json:"egressCallback"`
}

// EgressCallbackSection 是 proxied 模式下 daemon 把出站事件回投给宿主的地址。
type EgressCallbackSection struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// NormalizeIngressMode 归一化 ingress 模式，未知/空值回退到 standalone。
func NormalizeIngressMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case IngressProxied:
		return IngressProxied
	case IngressDirect:
		return IngressDirect
	default:
		return IngressStandalone
	}
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
	Ingress      IngressSection      `json:"ingress"`
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
		Ingress: IngressSection{Mode: IngressStandalone},
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

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

// ResolveCloudHost 返回灯效 / 灯效规则 / ASR 共用的云端主机（优先级见 resolveCloudHost）。
func ResolveCloudHost(cfg Config) string {
	host, _ := resolveCloudHost(cfg)
	return host
}

// ResolveCloudHostSource 返回主机及其来源（env|cloud.host|relay.url|default），
// 供 daemon status / doctor 说清楚"为什么打到这台机器"。
func ResolveCloudHostSource(cfg Config) (string, string) {
	return resolveCloudHost(cfg)
}

// resolveCloudHost 是主机来源的唯一决策点，灯效 / 灯效规则 / ASR / Relay 共用：
//
//  1. PHONE_NOTIFICATIONS_ENV / OPENCLAW_HOST_* 显式设过 —— 环境变量说了算
//  2. cloud.host
//  3. relay.url 里那个域名，且它是某个内置环境的默认域名
//  4. 内置默认（production）
//
// 第 3 条是回应一个真实故障：profile 里 relay.url 白纸黑字写着 -test，解析出来却是生产。
// 此前的规则是"relay.url 的 host 等于内置默认域名就当它还是默认值、按环境变量重解析"，
// 于是配置文件在骗读它的人，隧道和灯效一起跑去生产（灯效因此 401 Invalid plugin API Key）。
// 现在没设环境变量时就认这个域名；反过来第 1 条保证 `PHONE_NOTIFICATIONS_ENV=x yc ...`
// 这种临时翻环境仍然压得住配置。
//
// 第 3 条只认内置默认域名：relay.url 被改成自定义地址时（自建隧道 / 代理），那台机器未必
// 提供插件侧 API，把灯效 / ASR 一起指过去只会把 401 换成 404，所以自定义值只对隧道生效。
func resolveCloudHost(cfg Config) (string, string) {
	if envhost.Explicit() {
		return envhost.Host(), "env"
	}
	if host := envhost.Normalize(cfg.Cloud.Host); host != "" {
		return host, "cloud.host"
	}
	if host := relayHost(cfg.Relay.URL); envhost.IsDefaultHost(host) {
		return host, "relay.url"
	}
	return envhost.Host(), "default"
}

// relayHost 从 Relay URL 中取出纯主机名（去 scheme 与路径）。
func relayHost(url string) string {
	host := envhost.Normalize(url)
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	return host
}

// ResolveRelayURL 返回 daemon 实际应连接的 Relay 地址。
// 若配置里持久化的仍是某个环境的内置默认主机（而非用户自定义 URL），则回落到
// cloud.host —— 这样隧道与灯效 / ASR 天然指向同一套环境，不会出现"隧道连着但灯效
// 401"；用户显式改过的自定义 URL 始终保留，仍可单独把隧道指向别处。
func ResolveRelayURL(cfg Config) string {
	url := cfg.Relay.URL
	if url == "" || envhost.IsDefaultHost(relayHost(url)) {
		return "wss://" + ResolveCloudHost(cfg) + RelayPath
	}
	return url
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

// CloudSection 云端接口主机配置。空字符串表示跟随 PHONE_NOTIFICATIONS_ENV
// （见 internal/envhost），填了自定义主机则灯效 / 灯效规则 / ASR / Relay 一起改指。
type CloudSection struct {
	Host string `json:"host"`
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
	Cloud        CloudSection        `json:"cloud"`
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
		// Cloud.Host 留空：新 profile 默认跟随环境变量，不把当时的域名焊死进配置。
		Cloud: CloudSection{},
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

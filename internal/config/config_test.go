package config

import (
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/testutil"
)

func TestDefaultRelayURL(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	t.Setenv("OPENCLAW_HOST_TEST", "example.test")
	if got := DefaultRelayURL(); got != "wss://example.test/message/messages/ws/plugin" {
		t.Errorf("DefaultRelayURL = %q", got)
	}
}

func TestResolveRelayURL(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	t.Setenv("OPENCLAW_HOST_TEST", "")

	// 持久化的仍是另一环境的内置默认主机 → 跟随当前环境（test）重新解析。
	cfg := Config{Relay: RelaySection{URL: "wss://openclaw-service.yoooclaw.com" + RelayPath}}
	if got := ResolveRelayURL(cfg); got != "wss://openclaw-service-test.yoooclaw.com"+RelayPath {
		t.Errorf("default-host URL should follow env, got %q", got)
	}

	// 用户自定义 URL → 原样保留。
	custom := "wss://my-relay.example.com/custom/path"
	cfg.Relay.URL = custom
	if got := ResolveRelayURL(cfg); got != custom {
		t.Errorf("custom URL should be preserved, got %q", got)
	}

	// 空 URL → 回退当前环境默认。
	cfg.Relay.URL = ""
	if got := ResolveRelayURL(cfg); got != "wss://openclaw-service-test.yoooclaw.com"+RelayPath {
		t.Errorf("empty URL should fall back to env default, got %q", got)
	}
}

func TestDefaultShape(t *testing.T) {
	t.Parallel()
	cfg := Default("/creds.json")
	if cfg.Version != ConfigVersion {
		t.Errorf("version = %d", cfg.Version)
	}
	if cfg.Daemon.Port != DefaultPort || cfg.Daemon.Bind != DefaultBind {
		t.Errorf("daemon defaults wrong: %+v", cfg.Daemon)
	}
	if cfg.Auth.TokenRef != "file:/creds.json#gatewayToken" {
		t.Errorf("tokenRef = %q", cfg.Auth.TokenRef)
	}
	if cfg.Image.MaxBytes != DefaultImageMaxByte {
		t.Errorf("image maxBytes = %d", cfg.Image.MaxBytes)
	}
	if !cfg.Relay.Enabled || !cfg.LightRules.Enabled || !cfg.AutoUpdate.Enabled {
		t.Error("expected enabled defaults")
	}
}

func TestDefaultEvaluator(t *testing.T) {
	t.Parallel()
	ev := DefaultEvaluator("/c.json")
	if ev.Mode != "webhook" || ev.TimeoutMs != 5000 || ev.Retries != 1 {
		t.Errorf("evaluator defaults wrong: %+v", ev)
	}
	if !strings.HasSuffix(ev.WebhookSecretRef, "#evaluatorSecret") {
		t.Errorf("secretRef = %q", ev.WebhookSecretRef)
	}
}

func TestExistsSaveLoadRoundTrip(t *testing.T) {
	p := testutil.Sandbox(t)
	if Exists(p) {
		t.Fatal("config should not exist initially")
	}
	// Load 在缺文件时返回纯默认
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Daemon.Port != DefaultPort {
		t.Errorf("default load port = %d", loaded.Daemon.Port)
	}

	cfg := Default(p.Credentials)
	cfg.Daemon.Port = 19999
	cfg.Daemon.LogLevel = "debug"
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	if !Exists(p) {
		t.Fatal("config should exist after save")
	}
	loaded, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Daemon.Port != 19999 || loaded.Daemon.LogLevel != "debug" {
		t.Errorf("round trip mismatch: %+v", loaded.Daemon)
	}
}

func TestLoadMergesDefaults(t *testing.T) {
	p := testutil.Sandbox(t)
	// 只写部分字段，其余应由默认补齐（deepMerge 语义）。
	testutil.WriteFile(t, p.Config, []byte(`{"daemon":{"port":12345}}`))
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.Port != 12345 {
		t.Errorf("port not loaded: %d", cfg.Daemon.Port)
	}
	if cfg.Relay.URL == "" || cfg.Image.MaxBytes != DefaultImageMaxByte {
		t.Errorf("missing fields not defaulted: %+v", cfg)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	p := testutil.Sandbox(t)
	testutil.WriteFile(t, p.Config, []byte("{bad"))
	if _, err := Load(p); err == nil {
		t.Error("invalid config should error")
	}
}

func TestRequire(t *testing.T) {
	p := testutil.Sandbox(t)
	_, err := Require(p)
	var ye *errs.Error
	if e, ok := err.(*errs.Error); ok {
		ye = e
	}
	if ye == nil || ye.Code != errs.CodeConfigInvalid {
		t.Fatalf("Require on missing config should be CONFIG_INVALID, got %v", err)
	}
	if err := Save(p, Default(p.Credentials)); err != nil {
		t.Fatal(err)
	}
	if _, err := Require(p); err != nil {
		t.Errorf("Require after save should succeed: %v", err)
	}
}

func TestToMapAndLoadMergedMap(t *testing.T) {
	p := testutil.Sandbox(t)
	if err := Save(p, Default(p.Credentials)); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMergedMap(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := GetByPath(m, "daemon.port"); !ok {
		t.Error("merged map should contain daemon.port")
	}
	direct := ToMap(Default(p.Credentials))
	if _, ok := direct["relay"]; !ok {
		t.Error("ToMap should contain relay section")
	}
}

func TestLoadRawAndSaveRaw(t *testing.T) {
	p := testutil.Sandbox(t)
	// 缺文件时 LoadRaw 返回默认 map
	m, err := LoadRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	SetByPath(m, "daemon.port", float64(20002))
	if err := SaveRaw(p, m); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := GetByPath(reloaded, "daemon.port"); v != float64(20002) {
		t.Errorf("SaveRaw/LoadRaw round trip failed: %v", v)
	}
}

func TestMask(t *testing.T) {
	t.Parallel()
	m := map[string]any{
		"auth": map[string]any{"tokenRef": "inline:supersecret"},
		"lightRules": map[string]any{
			"evaluator": map[string]any{"webhookSecretRef": "file:/c.json#x"},
		},
	}
	masked := Mask(m)
	if v, _ := GetByPath(masked, "auth.tokenRef"); v != "inline:****" {
		t.Errorf("inline secret should be masked: %v", v)
	}
	// file: 引用不遮罩
	if v, _ := GetByPath(masked, "lightRules.evaluator.webhookSecretRef"); v != "file:/c.json#x" {
		t.Errorf("file ref should not be masked: %v", v)
	}
	// 原 map 不被改动
	if v, _ := GetByPath(m, "auth.tokenRef"); v != "inline:supersecret" {
		t.Error("Mask must not mutate input")
	}
}

func TestResolveCloudHost(t *testing.T) {
	// 环境变量显式设过 → 第 1 优先级，压过配置里的一切。
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	t.Setenv("OPENCLAW_HOST_TEST", "")
	overridden := Config{Cloud: CloudSection{Host: "my-cloud.example.com"}}
	if got, src := ResolveCloudHostSource(overridden); got != "openclaw-service-test.yoooclaw.com" || src != "env" {
		t.Errorf("explicit env should win over cloud.host, got %q from %q", got, src)
	}

	// 以下都在"环境变量没设"的前提下。
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")

	if got, src := ResolveCloudHostSource(Config{}); got != "openclaw-service.yoooclaw.com" || src != "default" {
		t.Errorf("bare config should fall back to built-in default, got %q from %q", got, src)
	}
	// cloud.host → 第 2 优先级。
	if got, src := ResolveCloudHostSource(overridden); got != "my-cloud.example.com" || src != "cloud.host" {
		t.Errorf("cloud.host should win, got %q from %q", got, src)
	}
	// scheme / 尾斜杠会被归一化掉。
	messy := Config{Cloud: CloudSection{Host: "https://my-cloud.example.com/"}}
	if got := ResolveCloudHost(messy); got != "my-cloud.example.com" {
		t.Errorf("host should be normalized, got %q", got)
	}
	// 显式写成某个内置默认域名也必须生效（不能被当成"还是默认值"忽略掉）。
	pinned := Config{Cloud: CloudSection{Host: "openclaw-service.yoooclaw.com"}}
	if got := ResolveCloudHost(pinned); got != "openclaw-service.yoooclaw.com" {
		t.Errorf("explicitly pinned default domain should be honored, got %q", got)
	}
}

// 复现并锁住那个真实故障：relay.url 写着 -test、cloud.host 没设、环境变量没设，
// 此前灯效与隧道双双跑去生产（灯效 401）。现在配置说什么就是什么。
func TestResolveCloudHostFollowsRelayURL(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")

	cfg := Config{Relay: RelaySection{URL: "wss://openclaw-service-test.yoooclaw.com" + RelayPath}}
	if got, src := ResolveCloudHostSource(cfg); got != "openclaw-service-test.yoooclaw.com" || src != "relay.url" {
		t.Errorf("cloud host should follow relay.url, got %q from %q", got, src)
	}
	if got := ResolveRelayURL(cfg); got != "wss://openclaw-service-test.yoooclaw.com"+RelayPath {
		t.Errorf("relay should stay on the host its own config names, got %q", got)
	}

	// cloud.host 更高优先级，能压过 relay.url。
	cfg.Cloud.Host = "openclaw-service-dev.yoooclaw.com"
	if got, src := ResolveCloudHostSource(cfg); got != "openclaw-service-dev.yoooclaw.com" || src != "cloud.host" {
		t.Errorf("cloud.host should outrank relay.url, got %q from %q", got, src)
	}

	// 自定义 relay 主机不外溢到灯效 / ASR：那台机器未必提供插件侧 API。
	custom := Config{Relay: RelaySection{URL: "wss://my-relay.example.com/custom/path"}}
	if got, src := ResolveCloudHostSource(custom); got != "openclaw-service.yoooclaw.com" || src != "default" {
		t.Errorf("custom relay host must not leak into cloud host, got %q from %q", got, src)
	}
	// 但隧道自己仍然用它。
	if got := ResolveRelayURL(custom); got != "wss://my-relay.example.com/custom/path" {
		t.Errorf("custom relay URL should be preserved, got %q", got)
	}
}

func TestResolveRelayURLFollowsCloudHost(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")

	// relay.url 还是内置默认形态 → 跟着 cloud.host 走，隧道与灯效/ASR 不会分家。
	cfg := Config{
		Cloud: CloudSection{Host: "openclaw-service-test.yoooclaw.com"},
		Relay: RelaySection{URL: "wss://openclaw-service.yoooclaw.com" + RelayPath},
	}
	if got := ResolveRelayURL(cfg); got != "wss://openclaw-service-test.yoooclaw.com"+RelayPath {
		t.Errorf("relay should derive from cloud.host, got %q", got)
	}
	if got := ResolveCloudHost(cfg); !strings.Contains(got, "-test.") {
		t.Errorf("cloud host mismatch: %q", got)
	}

	// relay.url 被显式改成自定义地址 → 仍然覆盖 cloud.host（保留单独指向的能力）。
	cfg.Relay.URL = "wss://my-relay.example.com/custom/path"
	if got := ResolveRelayURL(cfg); got != "wss://my-relay.example.com/custom/path" {
		t.Errorf("custom relay URL should still win, got %q", got)
	}
}

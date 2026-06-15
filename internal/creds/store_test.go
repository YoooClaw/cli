package creds

import (
	"testing"

	"github.com/YoooClaw/cli/internal/keychain"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/testutil"
)

// fakeKeychain 是内存假钥匙串，避免测试触碰真实系统钥匙串。
type fakeKeychain struct {
	available bool
	store     map[string]string
}

func key(service, account string) string { return service + "/" + account }

// setup 隔离 YOOOCLAW_HOME 并安装一个默认空、可用的假钥匙串，返回它以便断言/预置。
// 因为改写包级 seam 变量，使用 setup 的测试不可 t.Parallel()。
func setup(t *testing.T) *fakeKeychain {
	t.Helper()
	testutil.Sandbox(t)
	fk := &fakeKeychain{available: true, store: map[string]string{}}
	oldA, oldG, oldS := keychainAvailableFn, keychainGetFn, keychainSetFn
	keychainAvailableFn = func() bool { return fk.available }
	keychainGetFn = func(service, account string) keychain.Result {
		return keychain.Result{Available: fk.available, Value: fk.store[key(service, account)]}
	}
	keychainSetFn = func(service, account, value string) bool {
		if !fk.available {
			return false
		}
		fk.store[key(service, account)] = value
		return true
	}
	t.Cleanup(func() {
		keychainAvailableFn, keychainGetFn, keychainSetFn = oldA, oldG, oldS
	})
	return fk
}

func TestIsValidAPIKeyLabel(t *testing.T) {
	t.Parallel()
	valid := []string{"work", "key-1", "a", "x123"}
	for _, l := range valid {
		if !IsValidAPIKeyLabel(l) {
			t.Errorf("%q should be valid", l)
		}
	}
	invalid := []string{"", "ALL", "all", "legacy", "env", "keychain", "local", "Has Space", "tooooooooooooooooooooooooooooooong-label"}
	for _, l := range invalid {
		if IsValidAPIKeyLabel(l) {
			t.Errorf("%q should be invalid", l)
		}
	}
	if AssertValidAPIKeyLabel("all") == nil {
		t.Error("AssertValidAPIKeyLabel(all) should error")
	}
	if AssertValidAPIKeyLabel("ok") != nil {
		t.Error("AssertValidAPIKeyLabel(ok) should pass")
	}
}

func TestMaskSecret(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                 "",
		"short":            "***",
		"12345678":         "***",
		"sk-1234567890abcd": "sk-1***abcd",
	}
	for in, want := range cases {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateToken(t *testing.T) {
	t.Parallel()
	a := GenerateToken(16)
	if len(a) != 32 { // 16 bytes -> 32 hex chars
		t.Errorf("token len = %d", len(a))
	}
	if GenerateToken(0) == a {
		t.Error("tokens should differ / default length applies")
	}
	if len(GenerateToken(0)) != 64 {
		t.Error("default token should be 32 bytes (64 hex)")
	}
}

func TestSetAndResolveAPIKeyFile(t *testing.T) {
	setup(t)
	res, err := SetAPIKey("sk-file-key-123456", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "file" || res.Label != "default" {
		t.Errorf("set result: %+v", res)
	}
	got := ResolveAPIKey()
	if got.Value != "sk-file-key-123456" || got.Source != "file" {
		t.Errorf("resolve: %+v", got)
	}
}

func TestSetAPIKeyEmpty(t *testing.T) {
	setup(t)
	if _, err := SetAPIKey("   ", false); err == nil {
		t.Error("empty key should error")
	}
}

func TestSetAPIKeyKeychain(t *testing.T) {
	fk := setup(t)
	res, err := SetAPIKey("sk-kc-abcdefgh", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "keychain" {
		t.Errorf("expected keychain source: %+v", res)
	}
	if fk.store[key(KeychainService, KeychainAPIKeyAccount)] != "sk-kc-abcdefgh" {
		t.Error("key not written to fake keychain")
	}
	// 解析只看 credentials.json：keychain key 不再被采用，仅作 shadowed 提示
	if got := ResolveAPIKey(); got.Source != "none" || got.Value != "" {
		t.Errorf("keychain must not resolve: %+v", got)
	}
	if set := ResolveAPIKeyEntries(); !set.ShadowedKeychainPresent {
		t.Errorf("keychain should be reported as shadowed: %+v", set)
	}
}

func TestSetAPIKeyKeychainUnavailable(t *testing.T) {
	fk := setup(t)
	fk.available = false
	if _, err := SetAPIKey("sk-x12345678", true); err == nil {
		t.Error("unavailable keychain should error")
	}
}

func TestResolveAPIKeyEnvIgnored(t *testing.T) {
	setup(t)
	t.Setenv(APIKeyEnv, "sk-env-override")
	SetAPIKey("sk-file-key-12345678", false)
	got := ResolveAPIKey()
	// 环境变量不再参与解析，只认 credentials.json
	if got.Source != "file" || got.Value != "sk-file-key-12345678" {
		t.Errorf("file should win, env ignored: %+v", got)
	}
}

func TestAddRemoveSetDefaultMultiKey(t *testing.T) {
	setup(t)
	if _, err := AddAPIKey("sk-aaaa1111bbbb", "work", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAPIKey("sk-cccc2222dddd", "home", false, false); err != nil {
		t.Fatal(err)
	}
	set := ResolveAPIKeyEntries()
	if set.Mode != "file-multi" || len(set.Entries) != 2 {
		t.Fatalf("expected 2 file-multi entries: %+v", set)
	}
	// 第一条 work 默认
	if set.DefaultEntry == nil || set.DefaultEntry.Label != "work" {
		t.Errorf("default should be work: %+v", set.DefaultEntry)
	}
	// 切换默认到 home
	if _, err := SetDefaultAPIKey("home"); err != nil {
		t.Fatal(err)
	}
	if d := ResolveAPIKey(); d.Label != "home" {
		t.Errorf("default should now be home: %+v", d)
	}
	// 重复 label 无 force 报错
	if _, err := AddAPIKey("sk-eeee", "home", false, false); err == nil {
		t.Error("duplicate label without force should error")
	}
	// force 覆盖
	if _, err := AddAPIKey("sk-ffff9999gggg", "home", false, true); err != nil {
		t.Errorf("force overwrite should succeed: %v", err)
	}
	// 删除 home（是默认）-> work 接任默认
	_, removed, newDefault, err := RemoveAPIKey("home")
	if err != nil || removed != "home" {
		t.Fatalf("remove home: removed=%q err=%v", removed, err)
	}
	if newDefault != "work" {
		t.Errorf("new default should be work, got %q", newDefault)
	}
}

func TestAddAPIKeyInvalidInputs(t *testing.T) {
	setup(t)
	if _, err := AddAPIKey("", "work", false, false); err == nil {
		t.Error("empty key should error")
	}
	if _, err := AddAPIKey("sk-x", "BAD LABEL", false, false); err == nil {
		t.Error("invalid label should error")
	}
}

func TestRemoveSetDefaultNotFound(t *testing.T) {
	setup(t)
	if _, _, _, err := RemoveAPIKey("ghost"); err == nil {
		t.Error("removing missing label should error")
	}
	if _, err := SetDefaultAPIKey("ghost"); err == nil {
		t.Error("set-default missing label should error")
	}
}

func TestResolveAPIKeyNone(t *testing.T) {
	setup(t) // 空沙箱、空假钥匙串
	got := ResolveAPIKey()
	if got.Source != "none" {
		t.Errorf("expected none, got %+v", got)
	}
	if set := ResolveAPIKeyEntries(); set.Mode != "none" {
		t.Errorf("expected none mode, got %q", set.Mode)
	}
}

func TestResolveLegacyFileSingle(t *testing.T) {
	setup(t)
	// 直接写 legacy apiKey 字段（无 apiKeys[]）
	testutil.WriteFile(t, paths.SharedCredentialsPath(), []byte(`{"apiKey":"sk-legacy-12345678"}`))
	set := ResolveAPIKeyEntries()
	if set.Mode != "legacy-file-single" || !set.LegacyAPIKeyPresent {
		t.Errorf("expected legacy single: %+v", set)
	}
}

func TestNormalizeStoredEntriesWarnings(t *testing.T) {
	setup(t)
	// 含非法/重复/空 key 条目
	testutil.WriteFile(t, paths.SharedCredentialsPath(), []byte(`{"apiKeys":[
		{"label":"work","key":"sk-1111aaaa"},
		"not-an-object",
		{"label":"BAD LABEL","key":"x"},
		{"label":"empty","key":""},
		{"label":"work","key":"sk-dupe"}
	]}`))
	set := ResolveAPIKeyEntries()
	if len(set.Entries) != 1 || set.Entries[0].Label != "work" {
		t.Errorf("only valid work entry should survive: %+v", set.Entries)
	}
	if len(set.Warnings) == 0 {
		t.Error("expected warnings for skipped entries")
	}
}

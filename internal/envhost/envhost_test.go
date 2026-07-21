package envhost

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                      "",
		"  example.com  ":       "example.com",
		"https://example.com":   "example.com",
		"wss://example.com/":    "example.com",
		"http://example.com///": "example.com",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestName(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	if Name() != "test" {
		t.Error("should read test env")
	}
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "bogus")
	if Name() != "production" {
		t.Error("unknown env should fall back to production")
	}
}

func TestHostOverrideAndDefault(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "development")
	t.Setenv("OPENCLAW_HOST_DEVELOPMENT", "https://dev.override.test/")
	if got := Host(); got != "dev.override.test" {
		t.Errorf("override host = %q", got)
	}
	t.Setenv("OPENCLAW_HOST_DEVELOPMENT", "")
	if got := Host(); got != "openclaw-service-dev.yoooclaw.com" {
		t.Errorf("default dev host = %q", got)
	}
}

func TestIsDefaultHost(t *testing.T) {
	t.Parallel()
	if !IsDefaultHost("wss://openclaw-service.yoooclaw.com") {
		t.Error("production host should be recognized as default")
	}
	if !IsDefaultHost("openclaw-service-test.yoooclaw.com") {
		t.Error("test host should be recognized as default")
	}
	if IsDefaultHost("my-custom-relay.example.com") {
		t.Error("custom host should not be default")
	}
	if IsDefaultHost("") {
		t.Error("empty host should not be default")
	}
}

func TestExplicit(t *testing.T) {
	// 没设 → 不算显式（Name() 此时也返回 production，二者必须能区分开）。
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")
	if Explicit() {
		t.Error("unset env should not count as explicit")
	}
	if Name() != "production" {
		t.Error("unset env still resolves to production for Name()")
	}
	// 显式设成 production → 算显式，尽管值与缺省相同。
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	if !Explicit() {
		t.Error("explicit production should count as explicit")
	}
	// 无效值不算。
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "bogus")
	if Explicit() {
		t.Error("unknown env name should not count as explicit")
	}
	// 只设主机覆盖也算显式。
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "my.example.com")
	if !Explicit() {
		t.Error("host override alone should count as explicit")
	}
	if Host() != "my.example.com" {
		t.Errorf("host override should win: %q", Host())
	}
}

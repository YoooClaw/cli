package light

import (
	"strings"
	"testing"
)

func TestNormalizeHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                            "",
		"  example.com  ":             "example.com",
		"https://example.com":         "example.com",
		"wss://example.com/":          "example.com",
		"http://example.com///":       "example.com",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadEnvName(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	if loadEnvName() != "test" {
		t.Error("should read test env")
	}
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "bogus")
	if loadEnvName() != "production" {
		t.Error("unknown env should fall back to production")
	}
}

func TestEnvHostOverrideAndDefault(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "development")
	t.Setenv("OPENCLAW_HOST_DEVELOPMENT", "https://dev.override.test/")
	if got := envHost(); got != "dev.override.test" {
		t.Errorf("override host = %q", got)
	}
	t.Setenv("OPENCLAW_HOST_DEVELOPMENT", "")
	if got := envHost(); got != defaultEnvHosts["development"] {
		t.Errorf("default dev host = %q", got)
	}
}

func TestLightAPIURL(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")
	url := LightAPIURL()
	if !strings.HasPrefix(url, "https://") || !strings.HasSuffix(url, "/api/message/tob/sendMessage") {
		t.Errorf("LightAPIURL = %q", url)
	}
}

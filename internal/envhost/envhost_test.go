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

func TestNotificationIntelligenceURLs(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	t.Setenv("OPENCLAW_HOST_TEST", "")
	t.Setenv("NOTIFICATION_INTELLIGENCE_LIGHT_RULES_URL", "https://example.com/api/notification-intelligence/light-rules/")
	t.Setenv("NOTIFICATION_INTELLIGENCE_LIGHT_EFFECTS_SEND_URL", "")

	if got := NotificationIntelligenceLightRulesURL(); got != "https://example.com/api/plugin/notification-intelligence/light-rules" {
		t.Fatalf("rules url = %q", got)
	}
	if got := NotificationIntelligenceLightEffectsSendURL(); got != "https://example.com/api/plugin/notification-intelligence/light-effects/send" {
		t.Fatalf("effects url = %q", got)
	}

	t.Setenv("NOTIFICATION_INTELLIGENCE_LIGHT_EFFECTS_SEND_URL", "https://override.example/light/send")
	if got := NotificationIntelligenceLightEffectsSendURL(); got != "https://override.example/light/send/light-effects/send" {
		t.Fatalf("custom effects url = %q", got)
	}
}

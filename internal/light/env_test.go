package light

import (
	"strings"
	"testing"
)

func TestLightAPIURL(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")
	url := LightAPIURL("")
	if !strings.HasPrefix(url, "https://") || !strings.HasSuffix(url, "/api/plugin/notification-intelligence/light-effects/send") {
		t.Errorf("LightAPIURL = %q", url)
	}
}

func TestLightAPIURLFollowsEnv(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	t.Setenv("OPENCLAW_HOST_TEST", "")
	if got := LightAPIURL(""); !strings.Contains(got, "openclaw-service-test.yoooclaw.com") {
		t.Errorf("test env LightAPIURL = %q", got)
	}
}

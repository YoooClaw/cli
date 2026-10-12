package daemon

import (
	"github.com/YoooClaw/cli/internal/config"
	"testing"
)

func TestResolvedEnvironmentStatus(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")
	s := &server{cfg: config.Config{Relay: config.RelaySection{URL: "wss://openclaw-service-test.yoooclaw.com" + config.RelayPath}}}
	if s.cloudStatusPayload()["env"] != "test" || s.relayStatusPayload()["env"] != "test" {
		t.Fatal("resolved test host shown as production")
	}
	s.cfg.Relay.URL = "wss://private.example/relay"
	if s.relayStatusPayload()["env"] != "custom" {
		t.Fatal("custom host mislabeled")
	}
}

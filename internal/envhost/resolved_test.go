package envhost

import "testing"

func TestNameForResolvedHost(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	for input, want := range map[string]string{"openclaw-service-test.yoooclaw.com": "test", "wss://openclaw-service-dev.yoooclaw.com/message/messages/ws/plugin": "development", "https://private.example/api": "custom"} {
		if got := NameForHost(input); got != want {
			t.Fatalf("%s: %s != %s", input, got, want)
		}
	}
}

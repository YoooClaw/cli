package light

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepeatInputFromAny(t *testing.T) {
	t.Parallel()
	in := RepeatInputFromAny(true, 3.0)
	if in.Repeat == nil || !*in.Repeat || in.RepeatTimes == nil || *in.RepeatTimes != 3 {
		t.Errorf("RepeatInputFromAny full: %+v", in)
	}
	empty := RepeatInputFromAny("not-bool", "not-num")
	if empty.Repeat != nil || empty.RepeatTimes != nil {
		t.Errorf("non-typed values should be nil: %+v", empty)
	}
}

func TestLookupPreset(t *testing.T) {
	t.Parallel()
	presets := Presets()
	if len(presets) == 0 {
		t.Skip("no presets embedded")
	}
	first := presets[0].PresetID
	got, ok := LookupPreset(first)
	if !ok || got.PresetID != first {
		t.Errorf("LookupPreset(%q) failed", first)
	}
	if _, ok := LookupPreset("no-such-preset"); ok {
		t.Error("missing preset should return false")
	}
}

func TestResolveLightTitle(t *testing.T) {
	t.Parallel()
	if got := resolveLightTitle("  My Title ", "reason", nil); got != "My Title" {
		t.Errorf("explicit title should win: %q", got)
	}
	if got := resolveLightTitle("", "  Reason ", nil); got != "Reason" {
		t.Errorf("reason fallback: %q", got)
	}
	segs := []map[string]any{{"mode": "wave"}, {"mode": "steady"}}
	if got := resolveLightTitle("", "", segs); got != "Effect: wave+steady" {
		t.Errorf("mode-derived title: %q", got)
	}
	if got := resolveLightTitle("", "", []map[string]any{{}}); got != "Effect: custom" {
		t.Errorf("empty modes title: %q", got)
	}
}

func TestTruncateAndMaskKey(t *testing.T) {
	t.Parallel()
	if truncate("short", 10) != "short" {
		t.Error("short string unchanged")
	}
	if got := truncate("0123456789", 4); got != "0123…" {
		t.Errorf("truncate = %q", got)
	}
	if maskKey("") != "EMPTY" {
		t.Error("empty key -> EMPTY")
	}
	if maskKey("abcdefghijklmnopqrstuvwxyz") != truncate("abcdefghijklmnopqrstuvwxyz", 20) {
		t.Error("maskKey should truncate to 20")
	}
}

func TestNewUUID(t *testing.T) {
	t.Parallel()
	u := newUUID()
	if len(u) != 36 || strings.Count(u, "-") != 4 {
		t.Errorf("uuid format wrong: %q", u)
	}
	if u[14] != '4' {
		t.Errorf("uuid version nibble should be 4: %q", u)
	}
	if newUUID() == u {
		t.Error("uuids should be unique")
	}
}

func TestSendLightEffectConnectionError(t *testing.T) {
	// 指向一个会被拒绝的端口，覆盖 SendLightEffect 的请求构建与错误返回路径。
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "127.0.0.1:1")
	segs := []map[string]any{{"mode": "steady", "brightness": 100.0, "color": color(255, 0, 0)}}
	res := SendLightEffect("test-key", segs, RepeatInput{}, "reason", "title", nil)
	if res.OK {
		t.Error("connection to refused port should fail")
	}
	if res.Error == "" {
		t.Error("expected an error message")
	}
}

func TestSendLightEffectBadBody(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "127.0.0.1:1")
	// 空 segments 在发送前被本地校验拦下，不发起请求。
	res := SendLightEffect("k", nil, RepeatInput{}, "r", "t", nil)
	if res.OK {
		t.Error("empty segments should fail before send")
	}
}

func TestSendLightEffectUsesNotificationIntelligence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugin/notification-intelligence/light-effects/send" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key-Id") != "ock_test_key" {
			t.Fatalf("api key = %q", r.Header.Get("X-Api-Key-Id"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["bizUniqueId"] != "tool-call-1" || body["reason"] != "测试亮灯" {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true,"bizUniqueId":"server-id"}}`))
	}))
	defer server.Close()
	t.Setenv("NOTIFICATION_INTELLIGENCE_LIGHT_EFFECTS_SEND_URL", server.URL)

	segments := []map[string]any{{"mode": "steady", "duration_s": float64(1), "brightness": float64(100), "color": color(255, 0, 0)}}
	result := SendLightEffect("Bearer ock_test_key", segments, RepeatInput{}, "测试亮灯", "测试", nil, SendOptions{BizUniqueID: "tool-call-1"})
	if !result.OK || result.Via != "notification-intelligence" || result.BizUniqueID != "server-id" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSendLightEffectFallsBackToLegacy(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugin/notification-intelligence/light-effects/send":
			http.NotFound(w, r)
		case "/api/message/tob/sendMessage":
			_, _ = w.Write([]byte(`{"code":"000000","data":{"success":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	previousClient := lightHTTPClient
	lightHTTPClient = server.Client()
	defer func() { lightHTTPClient = previousClient }()
	t.Setenv("NOTIFICATION_INTELLIGENCE_LIGHT_EFFECTS_SEND_URL", server.URL)
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", server.URL)
	t.Setenv("LIGHT_TEMPLATE_ID", "template-1")

	segments := []map[string]any{{"mode": "steady", "duration_s": float64(1), "brightness": float64(100), "color": color(255, 0, 0)}}
	result := SendLightEffect("ock_test_key", segments, RepeatInput{}, "测试", "测试", nil)
	if !result.OK || result.Via != "message-service" || result.BizUniqueID == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSendLightEffectDoesNotFallbackOnBusinessFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"000000","data":{"success":false,"message":"rejected"}}`))
	}))
	defer server.Close()
	t.Setenv("NOTIFICATION_INTELLIGENCE_LIGHT_EFFECTS_SEND_URL", server.URL)
	t.Setenv("LIGHT_TEMPLATE_ID", "template-1")

	segments := []map[string]any{{"mode": "steady", "duration_s": float64(1), "brightness": float64(100), "color": color(255, 0, 0)}}
	result := SendLightEffect("ock_test_key", segments, RepeatInput{}, "测试", "测试", nil)
	if result.OK || result.Via != "notification-intelligence" || result.Error != "rejected" {
		t.Fatalf("result = %+v", result)
	}
}

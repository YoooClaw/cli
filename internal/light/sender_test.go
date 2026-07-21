package light

import (
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
	res := SendLightEffect("", "test-key", segs, RepeatInput{}, "reason", "title", nil)
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
	res := SendLightEffect("", "k", nil, RepeatInput{}, "r", "t", nil)
	if res.OK {
		t.Error("empty segments should fail before send")
	}
}

// LightAPIURL 恒定 https://，httptest 服务器进不来，故直接测装饰逻辑本身。
func TestDecorateSendError(t *testing.T) {
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	body := `{"code":"401","msg":"Invalid plugin API Key"}`

	decorated := decorateSendError(body, "ock-cli-abcdefgh")
	// 原始响应体保留，后面追加环境 + 遮罩 key + 下一步的排障指引。
	if !strings.HasPrefix(decorated, body) {
		t.Errorf("raw body should be kept as prefix: %s", decorated)
	}
	for _, want := range []string{"production", "openclaw-service.yoooclaw.com", "PHONE_NOTIFICATIONS_ENV"} {
		if !strings.Contains(decorated, want) {
			t.Errorf("401 message missing %q: %s", want, decorated)
		}
	}

	if got := decorateSendError(`{"msg":"boom"}`, "ock-cli-abcdefgh"); got != `{"msg":"boom"}` {
		t.Errorf("non-401 body should pass through untouched: %s", got)
	}
}

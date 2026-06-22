package yclib

import "testing"

func TestErrorFromEnvelope_Extracts(t *testing.T) {
	body := map[string]any{
		"ok":    false,
		"error": map[string]any{"code": "YOOOCLAW_NOT_FOUND", "message": "未知预设"},
	}
	err := errorFromEnvelope(404, body)
	if ErrorCode(err) != "YOOOCLAW_NOT_FOUND" {
		t.Fatalf("code = %q, want YOOOCLAW_NOT_FOUND", ErrorCode(err))
	}
	if err.Error() != "未知预设" {
		t.Fatalf("message = %q, want 未知预设", err.Error())
	}
}

func TestErrorFromEnvelope_Fallback(t *testing.T) {
	err := errorFromEnvelope(500, "plain text body")
	if ErrorCode(err) != CodeUnknown {
		t.Fatalf("code = %q, want %q", ErrorCode(err), CodeUnknown)
	}
}

func TestDecodeInto(t *testing.T) {
	src := map[string]any{"ok": true, "bizUniqueId": "abc", "status": float64(200)}
	var out LightSendResult
	if err := decodeInto(src, &out); err != nil {
		t.Fatalf("decodeInto: %v", err)
	}
	if !out.OK || out.BizUniqueID != "abc" || out.Status != 200 {
		t.Fatalf("decoded = %+v, want ok/abc/200", out)
	}
}

func TestGatewayToken_Resolution(t *testing.T) {
	// 显式 Credentials 优先。
	c, _ := New(Config{RootDir: t.TempDir(), Credentials: StaticCredentials{Token: "explicit"}})
	if got := c.gatewayToken(); got != "explicit" {
		t.Fatalf("explicit token = %q, want explicit", got)
	}
	// 无 Credentials 且未允许隐式 → 空。
	c2, _ := New(Config{RootDir: t.TempDir()})
	if got := c2.gatewayToken(); got != "" {
		t.Fatalf("no-creds token = %q, want empty", got)
	}
}

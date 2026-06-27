package recording

import (
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/testutil"
)

func TestValidateTransition(t *testing.T) {
	t.Parallel()
	if err := ValidateTransition(StatusSynced, StatusTranscribing); err != nil {
		t.Errorf("valid transition should pass: %v", err)
	}
	err := ValidateTransition(StatusSynced, StatusReceiving)
	if err == nil {
		t.Fatal("invalid transition should error")
	}
	if _, ok := err.(*TransitionError); !ok {
		t.Errorf("want TransitionError, got %T", err)
	}
	if err.Error() == "" {
		t.Error("error message should be non-empty")
	}
}

func TestCanStartTranscription(t *testing.T) {
	t.Parallel()
	for _, s := range []string{StatusSynced, StatusTranscribeFailed, StatusTranscribed} {
		if !CanStartTranscription(s) {
			t.Errorf("%s should allow transcription", s)
		}
	}
	if CanStartTranscription(StatusReceiving) {
		t.Error("receiving should not allow transcription")
	}
}

func TestIsRecordingStatus(t *testing.T) {
	t.Parallel()
	if !IsRecordingStatus(StatusSynced) || !IsRecordingStatus(StatusTranscribed) {
		t.Error("known statuses should be recognized")
	}
	if IsRecordingStatus("totally-made-up") {
		t.Error("unknown status should be false")
	}
}

func TestValidateAsrConfig(t *testing.T) {
	t.Parallel()
	if ValidateAsrConfig(nil) == "" {
		t.Error("nil config should be invalid")
	}
	if ValidateAsrConfig(&AsrConfig{Mode: "api"}) != "" {
		t.Error("api mode should be valid")
	}
	if ValidateAsrConfig(&AsrConfig{Mode: "local"}) == "" {
		t.Error("local mode should be rejected (deprecated)")
	}
	if ValidateAsrConfig(&AsrConfig{Mode: "bogus"}) == "" {
		t.Error("unknown mode should be invalid")
	}
	if !IsAsrConfigured(&AsrConfig{Mode: "api"}) {
		t.Error("api config should be configured")
	}
	if IsAsrConfigured(nil) {
		t.Error("nil should not be configured")
	}
}

func TestLoadLocalAsrConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if LoadLocalAsrConfig(dir) != nil {
		t.Error("missing config -> nil")
	}
	testutil.WriteFile(t, filepath.Join(dir, "asr-config.json"), []byte(`{"mode":"api","api":{"apiKey":"k1"}}`))
	cfg := LoadLocalAsrConfig(dir)
	if cfg == nil || cfg.Mode != "api" || cfg.API.APIKey != "k1" {
		t.Errorf("loaded config wrong: %+v", cfg)
	}
}

func TestResolveAsrConfig(t *testing.T) {
	t.Parallel()
	// caller 优先
	caller := &AsrConfig{Mode: "api", API: &AsrAPIConfig{APIKey: "caller-key"}}
	local := &AsrConfig{Mode: "api", API: &AsrAPIConfig{APIKey: "local-key"}}
	got := ResolveAsrConfig(caller, local, "fallback")
	if got.API.APIKey != "caller-key" {
		t.Errorf("caller key should win: %q", got.API.APIKey)
	}
	// caller nil -> local
	got = ResolveAsrConfig(nil, local, "fallback")
	if got.API.APIKey != "local-key" {
		t.Errorf("local key should be used: %q", got.API.APIKey)
	}
	// 空 apiKey -> 注入 fallback
	noKey := &AsrConfig{Mode: "api", API: &AsrAPIConfig{}}
	got = ResolveAsrConfig(noKey, nil, "fallback-key")
	if got.API.APIKey != "fallback-key" {
		t.Errorf("fallback key should be injected: %q", got.API.APIKey)
	}
	// 非 api mode 原样返回
	non := &AsrConfig{Mode: "yoooclaw"}
	if ResolveAsrConfig(non, nil, "x").Mode != "yoooclaw" {
		t.Error("non-api mode should pass through")
	}
	// 全 nil
	if ResolveAsrConfig(nil, nil, "x") != nil {
		t.Error("all nil -> nil")
	}
}

func TestInitializeAsr(t *testing.T) {
	testutil.Sandbox(t) // 隔离 creds（避免读到真实 api key）
	// 校验失败
	res := InitializeAsr(&AsrConfig{Mode: "local"})
	if res.OK {
		t.Error("local mode init should fail")
	}
	// 无 key
	res = InitializeAsr(&AsrConfig{Mode: "api", API: &AsrAPIConfig{}})
	if res.OK || res.KeyConfigured {
		t.Errorf("no key should fail init: %+v", res)
	}
	// 有 key
	res = InitializeAsr(&AsrConfig{Mode: "api", API: &AsrAPIConfig{APIKey: "k"}})
	if !res.OK || !res.KeyConfigured {
		t.Errorf("with key should succeed: %+v", res)
	}
}

func TestAPIValue(t *testing.T) {
	t.Parallel()
	var nilCfg *AsrConfig
	if nilCfg.APIValue().APIKey != "" {
		t.Error("nil config APIValue should be zero")
	}
	cfg := &AsrConfig{Mode: "api", API: &AsrAPIConfig{APIKey: "k"}}
	if cfg.APIValue().APIKey != "k" {
		t.Error("APIValue should return api copy")
	}
}

func TestParseTranscriptDocument(t *testing.T) {
	t.Parallel()
	good := []byte(`{"recordingId":"r1","generatedAt":"2026-06-07T10:00:00Z","source":{"provider":"model-proxy"},"normalized":{"text":"hi"}}`)
	doc, ok := ParseTranscriptDocument(good)
	if !ok || doc.RecordingID != "r1" {
		t.Errorf("valid doc should parse: %+v ok=%v", doc, ok)
	}
	// 缺必填字段
	if _, ok := ParseTranscriptDocument([]byte(`{"recordingId":"r1"}`)); ok {
		t.Error("missing required fields should fail")
	}
	// 非法 JSON
	if _, ok := ParseTranscriptDocument([]byte(`{bad`)); ok {
		t.Error("invalid json should fail")
	}
}

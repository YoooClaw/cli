package light

import (
	"encoding/base64"
	"testing"
)

// TestPresetGolden 用 presets-v1.json 自带的 base64 逐字节校验协议编码（body 的 UTF-8）。
func TestPresetGolden(t *testing.T) {
	if len(Presets()) == 0 {
		t.Fatal("no presets loaded")
	}
	for _, p := range Presets() {
		body, err := BuildLightEffectApnsBody(p.Segments, RepeatInput{RepeatTimes: f64(float64(p.RepeatTimes))}, "")
		if err != nil {
			t.Fatalf("%s: build error: %v", p.PresetID, err)
		}
		got := base64.StdEncoding.EncodeToString([]byte(body))
		if got != p.Base64 {
			t.Errorf("%s: golden mismatch\n want %s\n  got %s", p.PresetID, p.Base64, got)
		}
	}
}

func f64(v float64) *float64 { return &v }

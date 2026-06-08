package light

import "testing"

func color(r, g, b float64) map[string]any {
	return map[string]any{"r": r, "g": g, "b": b}
}

func TestValidateSegmentsTopLevel(t *testing.T) {
	t.Parallel()
	if ValidateSegments("not-array").Valid {
		t.Error("non-array should be invalid")
	}
	if ValidateSegments([]any{}).Valid {
		t.Error("empty should be invalid")
	}
	tooMany := make([]any, MaxSegments+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"mode": "steady", "brightness": 100.0, "color": color(1, 1, 1)}
	}
	if ValidateSegments(tooMany).Valid {
		t.Error("over MaxSegments should be invalid")
	}
}

func TestValidateSegmentsValidSteady(t *testing.T) {
	t.Parallel()
	res := ValidateSegments([]any{
		map[string]any{"mode": "steady", "brightness": 200.0, "color": color(255, 0, 0), "duration_s": 5.0},
	})
	if !res.Valid {
		t.Fatalf("valid steady should pass: %+v", res.Errors)
	}
	if len(res.Segments) != 1 {
		t.Errorf("expected 1 normalized segment")
	}
}

func TestValidateSegmentInvalidMode(t *testing.T) {
	t.Parallel()
	res := ValidateSegments([]any{map[string]any{"mode": "disco", "brightness": 1.0, "color": color(1, 1, 1)}})
	if res.Valid {
		t.Error("unsupported mode should fail")
	}
}

func TestValidateSegmentNotObject(t *testing.T) {
	t.Parallel()
	if ValidateSegments([]any{"nope"}).Valid {
		t.Error("non-object segment should fail")
	}
}

func TestValidateForegroundBrightnessZero(t *testing.T) {
	t.Parallel()
	// brightness=0 on non-steady should fail
	res := ValidateSegments([]any{map[string]any{"mode": "wave", "brightness": 0.0, "color": color(10, 10, 10)}})
	if res.Valid {
		t.Error("brightness=0 on wave should fail")
	}
	// brightness=0 on steady is allowed
	ok := ValidateSegments([]any{map[string]any{"mode": "steady", "brightness": 0.0, "color": color(10, 10, 10), "duration_s": 0.0}})
	if !ok.Valid {
		t.Errorf("brightness=0 on steady should pass: %+v", ok.Errors)
	}
}

func TestValidateColorAndRanges(t *testing.T) {
	t.Parallel()
	bad := ValidateSegments([]any{map[string]any{"mode": "steady", "brightness": 300.0, "color": color(999, -1, 0)}})
	if bad.Valid {
		t.Error("out-of-range brightness/color should fail")
	}
	noColor := ValidateSegments([]any{map[string]any{"mode": "steady", "brightness": 100.0, "color": "red"}})
	if noColor.Valid {
		t.Error("non-object color should fail")
	}
}

func TestValidateWaveOptions(t *testing.T) {
	t.Parallel()
	res := ValidateSegments([]any{map[string]any{
		"mode": "wave", "brightness": 100.0, "color": color(255, 0, 0), "duration_s": 3.0,
		"interval_ms": 200.0, "direction": "ltr", "window": 2.0,
		"background": map[string]any{"r": 0.0, "g": 0.0, "b": 10.0, "brightness": 50.0},
	}})
	if !res.Valid {
		t.Fatalf("valid wave should pass: %+v", res.Errors)
	}
	// bad direction / window
	bad := ValidateSegments([]any{map[string]any{
		"mode": "wave", "brightness": 100.0, "color": color(1, 1, 1),
		"direction": "up", "window": 9.0,
	}})
	if bad.Valid {
		t.Error("bad direction/window should fail")
	}
}

func TestValidateBreathTiming(t *testing.T) {
	t.Parallel()
	ok := ValidateSegments([]any{map[string]any{
		"mode": "breath", "brightness": 100.0, "color": color(1, 1, 1), "duration_s": 0.0,
		"breath_timing": map[string]any{"rise_ms": 100.0, "hold_ms": 0.0, "fall_ms": 100.0, "off_ms": 0.0},
	}})
	if !ok.Valid {
		t.Fatalf("valid breath should pass: %+v", ok.Errors)
	}
	bad := ValidateSegments([]any{map[string]any{
		"mode": "breath", "brightness": 100.0, "color": color(1, 1, 1),
		"breath_timing": map[string]any{"rise_ms": 0.0, "fall_ms": -1.0},
	}})
	if bad.Valid {
		t.Error("rise_ms=0 should fail")
	}
}

func TestValidatePixelFrame(t *testing.T) {
	t.Parallel()
	ok := ValidateSegments([]any{map[string]any{
		"mode": "pixel_frame", "duration_s": 0.0,
		"pixels": []any{
			map[string]any{"index": 0.0, "brightness": 100.0, "color": color(255, 0, 0)},
			map[string]any{"index": 1.0, "brightness": 100.0, "color": color(0, 255, 0)},
		},
	}})
	if !ok.Valid {
		t.Fatalf("valid pixel_frame should pass: %+v", ok.Errors)
	}
	// duplicate index + bad index + missing pixels
	dup := ValidateSegments([]any{map[string]any{
		"mode": "pixel_frame",
		"pixels": []any{
			map[string]any{"index": 0.0, "brightness": 100.0, "color": color(1, 1, 1)},
			map[string]any{"index": 0.0, "brightness": 100.0, "color": color(1, 1, 1)},
			map[string]any{"index": 9.0, "brightness": 100.0, "color": color(1, 1, 1)},
		},
	}})
	if dup.Valid {
		t.Error("duplicate/out-of-range pixel index should fail")
	}
	noPixels := ValidateSegments([]any{map[string]any{"mode": "pixel_frame", "pixels": "x"}})
	if noPixels.Valid {
		t.Error("non-array pixels should fail")
	}
}

func TestValidateColorFlow(t *testing.T) {
	t.Parallel()
	// 无任何非零颜色锚点 -> 失败
	bad := ValidateSegments([]any{map[string]any{"mode": "color_flow", "brightness": 100.0, "color": color(0, 0, 0)}})
	if bad.Valid {
		t.Error("color_flow without nonzero anchor should fail")
	}
	// 单一极端纯色前景，无底色 -> valid 但有 warning
	warn := ValidateSegments([]any{map[string]any{"mode": "color_flow", "brightness": 100.0, "color": color(255, 0, 0), "duration_s": 0.0}})
	if !warn.Valid {
		t.Fatalf("single anchor color_flow should still be valid: %+v", warn.Errors)
	}
	if len(warn.Warnings) == 0 || warn.Warnings[0].Code != "COLOR_FLOW_SINGLE_ANCHOR_MISUSE" {
		t.Errorf("expected single-anchor misuse warning: %+v", warn.Warnings)
	}
	// 前景 + 有效底色 -> 无 warning
	twoAnchor := ValidateSegments([]any{map[string]any{
		"mode": "color_flow", "brightness": 100.0, "color": color(255, 0, 0), "duration_s": 0.0,
		"background": map[string]any{"r": 0.0, "g": 0.0, "b": 255.0, "brightness": 100.0},
	}})
	if len(twoAnchor.Warnings) != 0 {
		t.Errorf("two-anchor color_flow should have no warning: %+v", twoAnchor.Warnings)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if stringifyMode(nil) != "undefined" {
		t.Error("nil mode -> undefined")
	}
	if joinSlash([]string{"a", "b", "c"}) != "a/b/c" {
		t.Error("joinSlash")
	}
	if trimNum(3.0) != "3" || trimNum(3.5) != "3.5" {
		t.Error("trimNum")
	}
	if !contains(validModes, "wave") || contains(validModes, "x") {
		t.Error("contains")
	}
}

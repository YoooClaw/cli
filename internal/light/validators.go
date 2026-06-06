package light

import (
	"fmt"
	"math"
)

// MaxSegments 是 segments 校验的上限（与 MaxLightSegments 同值）。
const MaxSegments = 12

// validModes 是允许的 segment 模式。
var validModes = []string{"wave", "breath", "strobe", "steady", "color_flow", "pixel_frame"}

// ValidationError 是单个字段的校验错误。
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationWarning 是不阻塞的告警（如 color_flow 单锚点误用）。
type ValidationWarning struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationResult 是 segments 校验结果。无效时 Errors 非空。
type ValidationResult struct {
	Valid    bool
	Segments []map[string]any
	Errors   []ValidationError
	Warnings []ValidationWarning
}

// ValidateSegments 校验 segments（接受解码后的 JSON 值）。
func ValidateSegments(raw any) ValidationResult {
	arr, ok := raw.([]any)
	if !ok {
		return invalid("segments", "必须是数组")
	}
	if len(arr) == 0 {
		return invalid("segments", "不能为空")
	}
	if len(arr) > MaxSegments {
		return invalid("segments", fmt.Sprintf("最多 %d 段", MaxSegments))
	}

	var errs []ValidationError
	var warnings []ValidationWarning
	for i, seg := range arr {
		validateSegment(seg, fmt.Sprintf("segments[%d]", i), &errs, &warnings)
	}
	if len(errs) > 0 {
		return ValidationResult{Valid: false, Errors: errs}
	}
	segs := make([]map[string]any, len(arr))
	for i, seg := range arr {
		segs[i], _ = seg.(map[string]any)
	}
	return ValidationResult{Valid: true, Segments: segs, Warnings: warnings}
}

func invalid(field, message string) ValidationResult {
	return ValidationResult{Valid: false, Errors: []ValidationError{{Field: field, Message: message}}}
}

func validateSegment(raw any, prefix string, errs *[]ValidationError, warnings *[]ValidationWarning) {
	seg, ok := raw.(map[string]any)
	if !ok {
		add(errs, prefix, "必须是对象")
		return
	}

	mode, _ := seg["mode"].(string)
	if !contains(validModes, mode) {
		add(errs, prefix+".mode", fmt.Sprintf("不支持的模式 '%v'，可选：%s", stringifyMode(seg["mode"]), joinSlash(validModes)))
	}

	validateNonNegativeNumber(seg["duration_s"], prefix+".duration_s", errs, "必须是 ≥0 的数字（0 表示无限时长）")

	switch mode {
	case "wave":
		validateForegroundSegment(seg, prefix, errs)
		validateOptionalNonNegativeNumber(seg["interval_ms"], prefix+".interval_ms", errs)
		validateOptionalDirection(seg["direction"], prefix+".direction", errs)
		validateOptionalWindow(seg["window"], prefix+".window", errs)
		validateOptionalBackground(seg["background"], prefix+".background", errs)
	case "color_flow":
		validateForegroundSegment(seg, prefix, errs)
		validateOptionalNonNegativeNumber(seg["interval_ms"], prefix+".interval_ms", errs)
		validateOptionalDirection(seg["direction"], prefix+".direction", errs)
		validateOptionalWindow(seg["window"], prefix+".window", errs)
		validateOptionalBackground(seg["background"], prefix+".background", errs)
		if !hasNonZeroRgb(seg["color"]) && !hasNonZeroRgb(seg["background"]) {
			add(errs, prefix, "color_flow 至少需要一组非零颜色锚点（color 或 background）")
		}
		detectColorFlowSingleAnchorMisuse(seg, prefix, warnings)
	case "breath":
		validateForegroundSegment(seg, prefix, errs)
		validateOptionalBreathTiming(seg["breath_timing"], prefix+".breath_timing", errs)
	case "strobe":
		validateForegroundSegment(seg, prefix, errs)
		validateOptionalNonNegativeNumber(seg["interval_ms"], prefix+".interval_ms", errs)
	case "steady":
		validateForegroundSegment(seg, prefix, errs)
	case "pixel_frame":
		validatePixelFrame(seg["pixels"], prefix+".pixels", errs)
	default:
		validateOptionalNonNegativeNumber(seg["brightness"], prefix+".brightness", errs)
		validateOptionalColor(seg["color"], prefix+".color", errs)
		validateOptionalNonNegativeNumber(seg["interval_ms"], prefix+".interval_ms", errs)
		validateOptionalDirection(seg["direction"], prefix+".direction", errs)
		validateOptionalWindow(seg["window"], prefix+".window", errs)
		validateOptionalBreathTiming(seg["breath_timing"], prefix+".breath_timing", errs)
		validateOptionalBackground(seg["background"], prefix+".background", errs)
	}
}

func validateForegroundSegment(seg map[string]any, prefix string, errs *[]ValidationError) {
	validateNumberInRange(seg["brightness"], prefix+".brightness", errs, 0, 255, "必须是 0–255 的数字")
	validateColor(seg["color"], prefix+".color", errs)

	mode, _ := seg["mode"].(string)
	if b, ok := seg["brightness"].(float64); mode != "steady" && ok && b == 0 {
		add(errs, prefix+".brightness", "brightness=0 仅 steady 模式允许；其它模式会在固件侧被过滤")
	}
}

func validatePixelFrame(value any, field string, errs *[]ValidationError) {
	arr, ok := value.([]any)
	if !ok {
		add(errs, field, "pixel_frame 必须提供 pixels 数组（1–7 项）")
		return
	}
	if len(arr) < 1 || len(arr) > 7 {
		add(errs, field, "pixels 必须为 1–7 项")
	}

	seen := map[int]bool{}
	for i, p := range arr {
		prefix := fmt.Sprintf("%s[%d]", field, i)
		pixel, ok := p.(map[string]any)
		if !ok {
			add(errs, prefix, "必须是对象")
			continue
		}
		idx, idxOK := pixel["index"].(float64)
		if !idxOK || idx != math.Trunc(idx) || idx < 0 || idx > 6 {
			add(errs, prefix+".index", "index 必须是 0–6 的整数")
		} else if seen[int(idx)] {
			add(errs, prefix+".index", fmt.Sprintf("index=%s 重复", trimNum(idx)))
		} else {
			seen[int(idx)] = true
		}
		validateNumberInRange(pixel["brightness"], prefix+".brightness", errs, 0, 255, "必须是 0–255 的数字")
		validateColor(pixel["color"], prefix+".color", errs)
	}
}

func validateOptionalBreathTiming(value any, field string, errs *[]ValidationError) {
	if value == nil {
		return
	}
	bt, ok := value.(map[string]any)
	if !ok {
		add(errs, field, "必须是对象")
		return
	}
	validatePositiveNumber(bt["rise_ms"], field+".rise_ms", errs, "rise_ms 必须是 >0 的数字（不支持 0ms）")
	validateNonNegativeNumber(bt["hold_ms"], field+".hold_ms", errs, "hold_ms 必须是 ≥0 的数字")
	validatePositiveNumber(bt["fall_ms"], field+".fall_ms", errs, "fall_ms 必须是 >0 的数字（不支持 0ms）")
	validateNonNegativeNumber(bt["off_ms"], field+".off_ms", errs, "off_ms 必须是 ≥0 的数字")
}

func validateOptionalBackground(value any, field string, errs *[]ValidationError) {
	if value == nil {
		return
	}
	bg, ok := value.(map[string]any)
	if !ok {
		add(errs, field, "必须包含 r/g/b/brightness 数值")
		return
	}
	validateColor(bg, field, errs)
	validateNumberInRange(bg["brightness"], field+".brightness", errs, 0, 255, "必须是 0–255 的数字")
}

func validateOptionalColor(value any, field string, errs *[]ValidationError) {
	if value == nil {
		return
	}
	validateColor(value, field, errs)
}

func validateColor(value any, field string, errs *[]ValidationError) {
	c, ok := value.(map[string]any)
	if !ok {
		add(errs, field, "必须包含 r/g/b 数值")
		return
	}
	validateNumberInRange(c["r"], field+".r", errs, 0, 255, "必须是 0–255 的数字")
	validateNumberInRange(c["g"], field+".g", errs, 0, 255, "必须是 0–255 的数字")
	validateNumberInRange(c["b"], field+".b", errs, 0, 255, "必须是 0–255 的数字")
}

func validateOptionalDirection(value any, field string, errs *[]ValidationError) {
	if value == nil {
		return
	}
	if d, ok := value.(string); !ok || (d != "ltr" && d != "rtl") {
		add(errs, field, "direction 必须是 ltr 或 rtl")
	}
}

func validateOptionalWindow(value any, field string, errs *[]ValidationError) {
	if value == nil {
		return
	}
	w, ok := value.(float64)
	if !ok || (w != 1 && w != 2 && w != 3) {
		add(errs, field, "window 仅支持 1/2/3")
	}
}

func validateOptionalNonNegativeNumber(value any, field string, errs *[]ValidationError) {
	if value == nil {
		return
	}
	validateNonNegativeNumber(value, field, errs, "必须是 ≥0 的数字")
}

func validatePositiveNumber(value any, field string, errs *[]ValidationError, message string) {
	if value == nil {
		return
	}
	if n, ok := value.(float64); !ok || !isFinite(n) || n <= 0 {
		add(errs, field, message)
	}
}

func validateNonNegativeNumber(value any, field string, errs *[]ValidationError, message string) {
	if n, ok := value.(float64); !ok || !isFinite(n) || n < 0 {
		add(errs, field, message)
	}
}

func validateNumberInRange(value any, field string, errs *[]ValidationError, min, max float64, message string) {
	if n, ok := value.(float64); !ok || !isFinite(n) || n < min || n > max {
		add(errs, field, message)
	}
}

func hasNonZeroRgb(value any) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"r", "g", "b"} {
		if n, ok := m[k].(float64); ok && isFinite(n) && n > 0 {
			return true
		}
	}
	return false
}

// detectColorFlowSingleAnchorMisuse 对「单一极端纯色前景锚点 + 无有效底色」推一条 warning。
func detectColorFlowSingleAnchorMisuse(seg map[string]any, prefix string, warnings *[]ValidationWarning) {
	fg := extractChannels(seg["color"])
	if fg == nil {
		return
	}
	bg := extractChannels(seg["background"])
	bgBrightness := 0.0
	if bgm, ok := seg["background"].(map[string]any); ok {
		if v, ok := bgm["brightness"].(float64); ok && isFinite(v) {
			bgBrightness = v
		}
	}
	bgActive := bg != nil && anyPositive(bg) && bgBrightness > 0
	if bgActive {
		return
	}
	var activeFg []float64
	for _, c := range fg {
		if c > 0 {
			activeFg = append(activeFg, c)
		}
	}
	if len(activeFg) != 1 || activeFg[0] < 192 {
		return
	}
	*warnings = append(*warnings, ValidationWarning{
		Field: prefix,
		Code:  "COLOR_FLOW_SINGLE_ANCHOR_MISUSE",
		Message: "color_flow 仅设置了单一极端纯色前景锚点（无有效底色锚点），实际效果是同色系亮暗环状流动，不是多色调色板流动。" +
			"若用户期望的是\"单色波浪\"，请改用 mode='wave'；若期望多色流动，请同时设置 background 作为第二锚点。",
	})
}

func extractChannels(value any) []float64 {
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make([]float64, 3)
	for i, k := range []string{"r", "g", "b"} {
		if n, ok := m[k].(float64); ok && isFinite(n) {
			out[i] = n
		}
	}
	return out
}

func anyPositive(vals []float64) bool {
	for _, v := range vals {
		if v > 0 {
			return true
		}
	}
	return false
}

func add(errs *[]ValidationError, field, message string) {
	*errs = append(*errs, ValidationError{Field: field, Message: message})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func joinSlash(list []string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += "/"
		}
		out += s
	}
	return out
}

func isFinite(v float64) bool {
	return !math.IsInf(v, 0) && !math.IsNaN(v)
}

func stringifyMode(v any) string {
	if v == nil {
		return "undefined"
	}
	return fmt.Sprintf("%v", v)
}

func trimNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int(v))
	}
	return fmt.Sprintf("%v", v)
}

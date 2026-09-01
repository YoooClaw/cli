package light

import (
	"fmt"
	"math"
	"strings"
)

// MaxLightSegments 是单次灯效组合的最大段数。
const MaxLightSegments = 12

// protocolDigits 把量化档位 v0–v11 映射到线协议字符（与固件约定一致）。
var protocolDigits = []rune{
	0x80, 0x81, 0x82, 0x83, 0x84, 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97,
}

//  = 播放一轮； = 无限循环。
const (
	ledSeparatorOnce = ""
	ledSeparatorLoop = ""
)

// 量化档位表（v0–v10 按下标映射；个别字段对 0 / 缺省做特判）。
var (
	durationStepsS            = []float64{0.5, 1, 2, 3, 5, 6, 8, 16, 24, 32, 48}
	intervalStepsMs           = []float64{50, 100, 200, 300, 500, 600, 800, 1600, 2400, 3200, 4800}
	breathStepsMs             = []float64{1040, 1560, 2080, 2600, 3100, 4160}
	brightnessSteps           = []float64{32, 64, 96, 128, 192, 255}
	colorSteps                = []float64{0, 32, 64, 128, 192, 255}
	backgroundBrightnessSteps = []float64{0, 32, 64, 96, 128, 192, 255}
)

var (
	multiChannelColorCoefficients = rgb{1, 0.25, 0.25}
	pureWhiteColorCoefficients    = rgb{1, 0.35, 0.35}
)

type rgb struct{ r, g, b float64 }

// modeToIndex 把 mode 映射到线协议的 M 值。color_flow 编码到 M=v4。
var modeToIndex = map[string]int{
	"wave": 0, "breath": 1, "strobe": 2, "steady": 3, "color_flow": 4,
}

// BuildLightEffectApnsBody 把 segments 编码成 APNs 可见文本 + 分隔符 + 线协议负载。
func BuildLightEffectApnsBody(segments []map[string]any, repeatInput RepeatInput, visibleTextOverride string) (string, error) {
	if len(segments) < 1 || len(segments) > MaxLightSegments {
		return "", fmt.Errorf("light_control supports 1-%d segments", MaxLightSegments)
	}
	if err := assertSegmentsValid(segments); err != nil {
		return "", err
	}
	repeatTimes, err := NormalizeRepeatTimes(repeatInput)
	if err != nil {
		return "", err
	}
	if err := AssertAncsRepeatTimes(repeatTimes); err != nil {
		return "", err
	}

	visibleText := resolveVisibleText(visibleTextOverride, segments)
	separator := ledSeparatorOnce
	if repeatTimes == 0 {
		separator = ledSeparatorLoop
	}
	var payload strings.Builder
	for _, seg := range segments {
		payload.WriteString(encodeSegment(seg))
	}
	return visibleText + separator + payload.String(), nil
}

func resolveVisibleText(override string, segments []map[string]any) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	return summarizeSegments(segments)
}

func summarizeSegments(segments []map[string]any) string {
	modes := make([]string, len(segments))
	for i, seg := range segments {
		modes[i], _ = seg["mode"].(string)
	}
	plural := ""
	if len(segments) > 1 {
		plural = "s"
	}
	return fmt.Sprintf("Effect: %s (%d segment%s)", strings.Join(modes, "+"), len(segments), plural)
}

func assertSegmentsValid(segments []map[string]any) error {
	anySegs := make([]any, len(segments))
	for i, s := range segments {
		anySegs[i] = s
	}
	res := ValidateSegments(anySegs)
	if res.Valid {
		return nil
	}
	parts := make([]string, len(res.Errors))
	for i, e := range res.Errors {
		parts[i] = e.Field + ": " + e.Message
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func encodeSegment(seg map[string]any) string {
	mode, _ := seg["mode"].(string)
	values := []int{modeToIndex[mode], quantizeDuration(numOr(seg, "duration_s", 0))}

	switch mode {
	case "wave", "color_flow":
		color := normalizeProtocolColor(mapField(seg, "color"))
		background := normalizeProtocolColor(mapField(seg, "background"))
		values = append(values,
			quantize(numOr(seg, "interval_ms", 200), intervalStepsMs),
			quantizeBrightnessValue(numOr(seg, "brightness", 0)),
			quantize(color.r, colorSteps), quantize(color.g, colorSteps), quantize(color.b, colorSteps),
			directionValue(seg),
			quantizeWindow(numOr(seg, "window", 2)),
			quantize(background.r, colorSteps), quantize(background.g, colorSteps), quantize(background.b, colorSteps),
			quantize(bgBrightness(seg), backgroundBrightnessSteps),
		)
	case "breath":
		color := normalizeProtocolColor(mapField(seg, "color"))
		bt := mapField(seg, "breath_timing")
		rise, riseOK := numField(bt, "rise_ms")
		hold, holdOK := numField(bt, "hold_ms")
		fall, fallOK := numField(bt, "fall_ms")
		off, offOK := numField(bt, "off_ms")
		values = append(values,
			quantizeBreathRiseFall(rise, riseOK),
			quantizeBreathHoldOff(hold, holdOK),
			quantizeBreathRiseFall(fall, fallOK),
			quantizeBreathHoldOff(off, offOK),
			quantizeBrightnessValue(numOr(seg, "brightness", 0)),
			quantize(color.r, colorSteps), quantize(color.g, colorSteps), quantize(color.b, colorSteps),
		)
	case "strobe":
		color := normalizeProtocolColor(mapField(seg, "color"))
		values = append(values,
			quantize(numOr(seg, "interval_ms", 200), intervalStepsMs),
			quantizeBrightnessValue(numOr(seg, "brightness", 0)),
			quantize(color.r, colorSteps), quantize(color.g, colorSteps), quantize(color.b, colorSteps),
		)
	case "steady":
		color := normalizeProtocolColor(mapField(seg, "color"))
		values = append(values,
			quantizeBrightnessValue(numOr(seg, "brightness", 0)),
			quantize(color.r, colorSteps), quantize(color.g, colorSteps), quantize(color.b, colorSteps),
		)
	}

	var b strings.Builder
	for _, v := range values {
		b.WriteRune(protocolDigits[v])
	}
	return b.String()
}

func directionValue(seg map[string]any) int {
	if d, ok := seg["direction"].(string); ok && d == "rtl" {
		return 1
	}
	return 0
}

func bgBrightness(seg map[string]any) float64 {
	v, _ := numField(mapField(seg, "background"), "brightness")
	return v
}

// normalizeProtocolColor 按通道数 / 纯白做色彩系数缩放（缺省通道按 0）。
func normalizeProtocolColor(color map[string]any) rgb {
	n := rgb{numOr(color, "r", 0), numOr(color, "g", 0), numOr(color, "b", 0)}
	if n.r == 255 && n.g == 255 && n.b == 255 {
		return applyColorCoefficients(n, pureWhiteColorCoefficients)
	}
	active := 0
	for _, c := range []float64{n.r, n.g, n.b} {
		if c > 0 {
			active++
		}
	}
	if active <= 1 {
		return n
	}
	return applyColorCoefficients(n, multiChannelColorCoefficients)
}

func applyColorCoefficients(c, k rgb) rgb {
	return rgb{scaleColorChannel(c.r, k.r), scaleColorChannel(c.g, k.g), scaleColorChannel(c.b, k.b)}
}

func scaleColorChannel(value, coefficient float64) float64 {
	return math.Max(0, math.Min(255, math.Round(value*coefficient)))
}

func quantize(value float64, steps []float64) int {
	bestIndex := 0
	bestDistance := math.Inf(1)
	for i, step := range steps {
		if d := math.Abs(value - step); d < bestDistance {
			bestIndex = i
			bestDistance = d
		}
	}
	return bestIndex
}

func quantizeDuration(durationS float64) int {
	if durationS == 0 {
		return 11
	}
	return quantize(durationS, durationStepsS)
}

func quantizeBrightnessValue(brightness float64) int {
	if brightness == 0 {
		return 11
	}
	return quantize(brightness, brightnessSteps)
}

func quantizeBreathRiseFall(value float64, present bool) int {
	if !present {
		value = 1040
	}
	return quantize(value, breathStepsMs)
}

func quantizeBreathHoldOff(value float64, present bool) int {
	if present && value == 0 {
		return 5
	}
	if !present {
		value = 1040
	}
	return quantize(value, breathStepsMs[:5])
}

func quantizeWindow(value float64) int {
	return int(value) - 1
}

// ── map/JSON 读取小工具 ──

func numOr(m map[string]any, key string, def float64) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return def
}

func numField(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	if v, ok := m[key].(float64); ok {
		return v, true
	}
	return 0, false
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

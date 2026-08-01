package recording

import (
	"fmt"
	"math"
)

func normalizeDurationSeconds(seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return math.Floor(seconds)
}

// FormatDurationDisplay 把秒数转换为适合用户和 Agent 阅读的带单位时长。
// 不足一分钟只显示秒；不足一小时不显示小时；进入更高一级后保留低位零值。
func FormatDurationDisplay(seconds float64) string {
	total := int64(normalizeDurationSeconds(seconds))
	hours := total / 3600
	minutes := (total % 3600) / 60
	secs := total % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, secs)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

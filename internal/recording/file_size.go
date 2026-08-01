package recording

import (
	"fmt"
	"math"
)

var shortFileSizeUnits = [...]string{"B", "kB", "MB", "GB", "TB", "PB"}

// FormatShortFileSize 按 Android Formatter.formatShortFileSize 的短格式口径
// 输出十进制文件大小：单位按 1000 换算，超过 900 时进位，舍入使用 HALF_UP。
// 为贴合 App 展示，舍入后为整数时不保留 .0。
func FormatShortFileSize(sizeBytes int64) string {
	if sizeBytes < 0 {
		return "--"
	}

	unitIndex := 0
	multiplier := int64(1)
	for unitIndex < len(shortFileSizeUnits)-1 && float64(sizeBytes)/float64(multiplier) > 900 {
		unitIndex++
		multiplier *= 1000
	}

	digits := 0
	switch {
	case multiplier == 1:
		digits = 0
	case sizeBytes < multiplier:
		digits = 2
	case sizeBytes < 10*multiplier:
		digits = 1
	default:
		digits = 0
	}

	scale := int64(math.Pow10(digits))
	whole := sizeBytes / multiplier
	remainder := sizeBytes % multiplier
	roundedScaled := whole*scale + (remainder*scale+multiplier/2)/multiplier
	if roundedScaled%scale == 0 {
		return fmt.Sprintf("%d %s", roundedScaled/scale, shortFileSizeUnits[unitIndex])
	}
	if digits == 1 {
		return fmt.Sprintf("%d.%d %s",
			roundedScaled/scale, roundedScaled%scale, shortFileSizeUnits[unitIndex])
	}
	return fmt.Sprintf("%d.%02d %s",
		roundedScaled/scale, roundedScaled%scale, shortFileSizeUnits[unitIndex])
}

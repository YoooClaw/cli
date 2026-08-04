package voice

import (
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

// ParseBoundary 解析带时区的 ISO 8601 时间或本地自然日。
func ParseBoundary(raw, flagName string) (*time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return &parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return &parsed, nil
	}
	return nil, errs.Newf(
		errs.CodeInvalidArgument,
		"%s 必须是带时区的 ISO 8601 时间或 YYYY-MM-DD（收到 %q）",
		flagName,
		raw,
	)
}

// ParseLocalDate 解析 usage_daily 使用的 YYYY-MM-DD 日期边界。
func ParseLocalDate(raw, flagName string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", errs.Newf(errs.CodeInvalidArgument, "%s 必须是 YYYY-MM-DD（收到 %q）", flagName, raw)
	}
	return value, nil
}

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

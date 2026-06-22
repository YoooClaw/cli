package notif

import (
	"regexp"
	"sort"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

// Count 是一个聚合计数项（维度取值 + 出现次数）。其 JSON 形态 {key,count}
// 与历史 CLI 输出逐字节一致，作为 CLI / library 共享的聚合原语。
type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// TopCounts 按 pick 维度聚合计数，按 (count 降序, key 升序) 排序后取前 topN。
// 空 key 不计入。CLI 的 summary/stats 与 library 共用本函数，确保口径一致。
func TopCounts(items []StoredNotification, pick func(StoredNotification) string, topN int) []Count {
	counts := map[string]int{}
	for _, item := range items {
		if k := pick(item); k != "" {
			counts[k]++
		}
	}
	rows := make([]Count, 0, len(counts))
	for k, v := range counts {
		rows = append(rows, Count{Key: k, Count: v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Key < rows[j].Key
	})
	if topN < len(rows) {
		rows = rows[:topN]
	}
	return rows
}

// AppLabel 取应用展示名（显示名优先，回退包名）。
func AppLabel(n StoredNotification) string { return firstNonEmpty(n.AppDisplayName, n.AppName) }

// SenderLabel 取发送人展示名（结构化发送人优先，回退标题）。
func SenderLabel(n StoredNotification) string { return firstNonEmpty(n.SenderName, n.Title) }

// ClientLabelOf 取来源客户端 label，旧数据回退 "legacy"。
func ClientLabelOf(n StoredNotification) string { return firstNonEmpty(n.ClientLabel, "legacy") }

// TsLocalDate 把 ISO 时间戳转本地日期 key（YYYY-MM-DD）；非法返回空串。
func TsLocalDate(ts string) string {
	t, ok := ParseTime(ts)
	if !ok {
		return ""
	}
	return t.In(time.Local).Format("2006-01-02")
}

// TsLocalHour 把 ISO 时间戳转本地小时（00-23）；非法返回空串。
func TsLocalHour(ts string) string {
	t, ok := ParseTime(ts)
	if !ok {
		return ""
	}
	return t.In(time.Local).Format("15")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var dateOnlyRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var isoTimeRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$`)

// StatsBoundary 是 stats 查询的一端边界，支持「仅日期」或「ISO 时间」两种输入，
// 同时给出精确时间戳与覆盖的日期 key 区间（用于按天文件早停过滤）。
type StatsBoundary struct {
	Raw        string
	ExactTs    *time.Time
	StartTs    time.Time
	EndTs      time.Time
	MinDateKey string
	MaxDateKey string
}

// ParseStatsBoundary 解析 stats 的 --from/--to 边界值。optionName 仅用于错误提示。
func ParseStatsBoundary(value, optionName string) (StatsBoundary, error) {
	if dateOnlyRE.MatchString(value) {
		t, err := time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			return StatsBoundary{}, errs.Newf(errs.CodeInvalidArgument, "%s 必须是合法日期 YYYY-MM-DD", optionName)
		}
		start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
		end := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999000000, time.Local)
		return StatsBoundary{Raw: value, StartTs: start, EndTs: end, MinDateKey: value, MaxDateKey: value}, nil
	}
	if !isoTimeRE.MatchString(value) {
		return StatsBoundary{}, errs.Newf(errs.CodeInvalidArgument,
			"%s 必须是 YYYY-MM-DD 或 ISO 8601 时间，例如 2026-06-02 或 2026-06-02T09:00:00+08:00", optionName)
	}
	exact, ok := ParseTime(value)
	if !ok {
		return StatsBoundary{}, errs.Newf(errs.CodeInvalidArgument, "%s 不是合法时间", optionName)
	}
	declaredKey := value[:10]
	localKey := exact.In(time.Local).Format("2006-01-02")
	lo, hi := declaredKey, localKey
	if lo > hi {
		lo, hi = hi, lo
	}
	e := exact
	return StatsBoundary{Raw: value, ExactTs: &e, StartTs: exact, EndTs: exact, MinDateKey: lo, MaxDateKey: hi}, nil
}

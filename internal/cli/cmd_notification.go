package cli

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/spf13/cobra"
)

// maxLimit 充当 summary/stats 的"全部"上限（对齐 TS 的 MAX_SAFE_INTEGER 语义）。
const maxLimit = 1 << 60

func addQueryFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("from", "", "开始时间，如 2026-03-01T09:00:00+08:00")
	f.String("to", "", "结束时间")
	f.String("app", "", "按应用过滤（支持中英文别名）")
	f.String("sender", "", "按发送人/标题过滤")
	f.String("conversation-type", "", "会话类型 group|private")
	f.String("keyword", "", "在标题/内容/发送人/会话名中搜索")
	f.String("client", "", "按 clientLabel 过滤；all 为全部")
	f.String("limit", "100", "最大返回条数")
}

func rawQueryFromCmd(cmd *cobra.Command) notif.RawQueryOpts {
	return notif.RawQueryOpts{
		From: flagStr(cmd, "from"), To: flagStr(cmd, "to"), App: flagStr(cmd, "app"),
		Sender: flagStr(cmd, "sender"), ConversationType: flagStr(cmd, "conversation-type"),
		Keyword: flagStr(cmd, "keyword"), Client: flagStr(cmd, "client"), Limit: flagStr(cmd, "limit"),
	}
}

func newNotificationCmd() *cobra.Command {
	c := &cobra.Command{Use: "notification", Short: "通知查询 🟢"}

	search := &cobra.Command{Use: "search", Short: "按筛选条件查询通知，时间倒序", Args: cobra.NoArgs, RunE: run(notificationSearch)}
	addQueryFlags(search)

	summary := &cobra.Command{Use: "summary", Short: "聚合统计 + 样例摘要，供 Agent 总结", Args: cobra.NoArgs, RunE: run(notificationSummary)}
	addQueryFlags(summary)
	summary.Flags().String("sample", "30", "返回最近样例条数")
	summary.Flags().String("top", "10", "聚合榜单条数")

	stats := &cobra.Command{Use: "stats", Short: "按维度聚合统计", Args: cobra.NoArgs, RunE: run(notificationStats)}
	stats.Flags().String("from", "", "YYYY-MM-DD 或 ISO 8601，默认 7 天前")
	stats.Flags().String("to", "", "YYYY-MM-DD 或 ISO 8601，默认今天")
	stats.Flags().String("app", "", "仅统计指定应用")
	stats.Flags().String("sender", "", "仅统计指定发送人/标题")
	stats.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	stats.Flags().String("dim", "all", "date|app|sender|hour|client|all")

	storagePath := &cobra.Command{Use: "storage-path", Short: "打印 notifications 目录绝对路径", Args: cobra.NoArgs, RunE: run(notificationStoragePath)}

	today := &cobra.Command{Use: "+today", Short: "今日通知摘要", Args: cobra.NoArgs, RunE: run(notificationToday)}
	today.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	recent := &cobra.Command{Use: "+recent", Short: "最近 1 小时通知", Args: cobra.NoArgs, RunE: run(notificationRecent)}
	recent.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	unread := &cobra.Command{Use: "+unread", Short: "（预留）未读通知", Args: cobra.NoArgs, RunE: run(notificationUnread)}

	c.AddCommand(search, summary, stats, storagePath, today, recent, unread)
	return c
}

func notificationSearch(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	opts, err := notif.BuildQueryOptions(rawQueryFromCmd(cmd), 100)
	if err != nil {
		return nil, err
	}
	return toAnySlice(notif.Query(ctx.Paths.Notifications, opts)), nil
}

func notificationSummary(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	sample := atoiDefault(flagStr(cmd, "sample"), 30)
	top := atoiDefault(flagStr(cmd, "top"), 10)
	raw := rawQueryFromCmd(cmd)
	raw.Limit = ""
	opts, err := notif.BuildQueryOptions(raw, maxLimit)
	if err != nil {
		return nil, err
	}
	items := notif.Query(ctx.Paths.Notifications, opts)
	return map[string]any{
		"ok":    true,
		"total": len(items),
		"range": map[string]any{"from": nilIfEmpty(raw.From), "to": nilIfEmpty(raw.To)},
		"topApps": topCounts(items, func(n notif.StoredNotification) string {
			return firstNonEmpty(n.AppDisplayName, n.AppName)
		}, top),
		"topSenders": topCounts(items, func(n notif.StoredNotification) string {
			return firstNonEmpty(n.SenderName, n.Title)
		}, top),
		"sample": toAnySlice(sliceN(items, sample)),
	}, nil
}

var dateOnlyRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var isoTimeRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$`)

type statsBoundary struct {
	raw        string
	exactTs    *time.Time
	startTs    time.Time
	endTs      time.Time
	minDateKey string
	maxDateKey string
}

func parseStatsBoundary(value, optionName string) (statsBoundary, error) {
	if dateOnlyRE.MatchString(value) {
		t, err := time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			return statsBoundary{}, errs.Newf(errs.CodeInvalidArgument, "%s 必须是合法日期 YYYY-MM-DD", optionName)
		}
		start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
		end := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999000000, time.Local)
		return statsBoundary{raw: value, startTs: start, endTs: end, minDateKey: value, maxDateKey: value}, nil
	}
	if !isoTimeRE.MatchString(value) {
		return statsBoundary{}, errs.Newf(errs.CodeInvalidArgument,
			"%s 必须是 YYYY-MM-DD 或 ISO 8601 时间，例如 2026-06-02 或 2026-06-02T09:00:00+08:00", optionName)
	}
	exact, ok := notif.ParseTime(value)
	if !ok {
		return statsBoundary{}, errs.Newf(errs.CodeInvalidArgument, "%s 不是合法时间", optionName)
	}
	declaredKey := value[:10]
	localKey := exact.In(time.Local).Format("2006-01-02")
	lo, hi := declaredKey, localKey
	if lo > hi {
		lo, hi = hi, lo
	}
	e := exact
	return statsBoundary{raw: value, exactTs: &e, startTs: exact, endTs: exact, minDateKey: lo, maxDateKey: hi}, nil
}

func notificationStats(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	fromRaw := flagStr(cmd, "from")
	if fromRaw == "" {
		fromRaw = localDaysAgo(7)
	}
	toRaw := flagStr(cmd, "to")
	if toRaw == "" {
		toRaw = localToday()
	}
	from, err := parseStatsBoundary(fromRaw, "--from")
	if err != nil {
		return nil, err
	}
	to, err := parseStatsBoundary(toRaw, "--to")
	if err != nil {
		return nil, err
	}
	if from.startTs.After(to.endTs) {
		return nil, errs.New(errs.CodeInvalidArgument, "--from 不能晚于 --to")
	}
	dim := flagStr(cmd, "dim")
	if dim == "" {
		dim = "all"
	}
	allowed := map[string]bool{"date": true, "app": true, "sender": true, "hour": true, "client": true, "all": true}
	if !allowed[dim] {
		return nil, errs.New(errs.CodeInvalidArgument, "--dim 只能是 date|app|sender|hour|client|all")
	}
	opts := notif.QueryOptions{
		App: flagStr(cmd, "app"), Sender: flagStr(cmd, "sender"), Client: flagStr(cmd, "client"),
		Limit: maxLimit, FromTs: from.exactTs, ToTs: to.exactTs,
		FromDateKey: from.minDateKey, ToDateKey: to.maxDateKey,
	}
	items := notif.Query(ctx.Paths.Notifications, opts)

	dims := map[string]any{
		"date":   topCounts(items, func(n notif.StoredNotification) string { return tsLocalDate(n.Timestamp) }, maxLimit),
		"app":    topCounts(items, func(n notif.StoredNotification) string { return firstNonEmpty(n.AppDisplayName, n.AppName) }, maxLimit),
		"sender": topCounts(items, func(n notif.StoredNotification) string { return firstNonEmpty(n.SenderName, n.Title) }, maxLimit),
		"hour":   topCounts(items, func(n notif.StoredNotification) string { return tsLocalHour(n.Timestamp) }, maxLimit),
		"client": topCounts(items, func(n notif.StoredNotification) string { return firstNonEmpty(n.ClientLabel, "legacy") }, maxLimit),
	}
	out := map[string]any{
		"ok": true, "total": len(items),
		"range": map[string]any{"from": from.raw, "to": to.raw}, "dim": dim,
	}
	if dim == "all" {
		for k, v := range dims {
			out[k] = v
		}
	} else {
		out[dim] = dims[dim]
	}
	return out, nil
}

func notificationStoragePath(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return map[string]any{"ok": true, "path": ctx.Paths.Notifications}, nil
}

func notificationToday(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	day := localToday()
	tz := localTZOffset()
	raw := notif.RawQueryOpts{From: day + "T00:00:00" + tz, To: day + "T23:59:59" + tz, Client: flagStr(cmd, "client"), Limit: ""}
	opts, err := notif.BuildQueryOptions(raw, maxLimit)
	if err != nil {
		return nil, err
	}
	return toAnySlice(notif.Query(ctx.Paths.Notifications, opts)), nil
}

func notificationRecent(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	fromTime := time.Now().Add(-time.Hour)
	opts := notif.QueryOptions{
		Limit: maxLimit, Client: flagStr(cmd, "client"), FromTs: &fromTime,
		FromDateKey: fromTime.In(time.Local).Format("2006-01-02"),
	}
	return toAnySlice(notif.Query(ctx.Paths.Notifications, opts)), nil
}

func notificationUnread(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return nil, errs.New(errs.CodeNotImplemented, "+unread 预留：需要先落地通知的已读状态模型")
}

// ── helpers ──

func topCounts(items []notif.StoredNotification, pick func(notif.StoredNotification) string, topN int) []any {
	counts := map[string]int{}
	for _, item := range items {
		if k := pick(item); k != "" {
			counts[k]++
		}
	}
	type kc struct {
		key   string
		count int
	}
	rows := make([]kc, 0, len(counts))
	for k, v := range counts {
		rows = append(rows, kc{k, v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].key < rows[j].key
	})
	if topN < len(rows) {
		rows = rows[:topN]
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"key": r.key, "count": r.count})
	}
	return out
}

func toAnySlice(items []notif.StoredNotification) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, it)
	}
	return out
}

func sliceN(items []notif.StoredNotification, n int) []notif.StoredNotification {
	if n < len(items) {
		return items[:n]
	}
	return items
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

func localToday() string        { return time.Now().Format("2006-01-02") }
func localDaysAgo(n int) string { return time.Now().AddDate(0, 0, -n).Format("2006-01-02") }
func tsLocalDate(ts string) string {
	t, ok := notif.ParseTime(ts)
	if !ok {
		return ""
	}
	return t.In(time.Local).Format("2006-01-02")
}
func tsLocalHour(ts string) string {
	t, ok := notif.ParseTime(ts)
	if !ok {
		return ""
	}
	return t.In(time.Local).Format("15")
}

func localTZOffset() string {
	_, offset := time.Now().Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hh := offset / 3600
	mm := (offset % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hh, mm)
}

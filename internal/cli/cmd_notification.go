package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/spf13/cobra"
)

// maxLimit 充当 summary/stats 的"全部"上限（对齐 TS 的 MAX_SAFE_INTEGER 语义）。
const maxLimit = 1 << 60

func addQueryFlags(cmd *cobra.Command) { addQueryFlagsWithLimit(cmd, "100") }

func addQueryFlagsWithLimit(cmd *cobra.Command, defaultLimit string) {
	f := cmd.Flags()
	f.String("from", "", "开始时间，如 2026-03-01T09:00:00+08:00")
	f.String("to", "", "结束时间")
	f.String("app", "", "按应用过滤（支持中英文别名）")
	f.String("sender", "", "按发送人/标题过滤")
	f.String("conversation-type", "", "会话类型 group|private")
	f.String("keyword", "", "在标题/内容/发送人/会话名中搜索")
	f.String("client", "", "按 clientLabel 过滤；all 为全部")
	f.String("limit", defaultLimit, "最大返回条数")
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

	c.AddCommand(search, summary, newSummaryJobCmd(), stats, storagePath, today, recent, unread)
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
	// 显式传 --limit 时聚合最近 N 条（供「总结约 X 条」场景）；
	// 不传时维持原行为：聚合范围内全部通知。
	limitRaw := raw.Limit
	raw.Limit = ""
	opts, err := notif.BuildQueryOptions(raw, maxLimit)
	if err != nil {
		return nil, err
	}
	if cmd.Flags().Changed("limit") {
		n, err := strconv.Atoi(limitRaw)
		if err != nil || n <= 0 {
			return nil, errs.New(errs.CodeInvalidArgument, "--limit 必须是大于 0 的整数")
		}
		opts.Limit = n
	}
	items := notif.Query(ctx.Paths.Notifications, opts)
	return map[string]any{
		"ok":         true,
		"total":      len(items),
		"range":      map[string]any{"from": nilIfEmpty(raw.From), "to": nilIfEmpty(raw.To)},
		"topApps":    notif.TopCounts(items, notif.AppLabel, top),
		"topSenders": notif.TopCounts(items, notif.SenderLabel, top),
		"sample":     toAnySlice(sliceN(items, sample)),
	}, nil
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
	from, err := notif.ParseStatsBoundary(fromRaw, "--from")
	if err != nil {
		return nil, err
	}
	to, err := notif.ParseStatsBoundary(toRaw, "--to")
	if err != nil {
		return nil, err
	}
	if from.StartTs.After(to.EndTs) {
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
		Limit: maxLimit, FromTs: from.ExactTs, ToTs: to.ExactTs,
		FromDateKey: from.MinDateKey, ToDateKey: to.MaxDateKey,
	}
	items := notif.Query(ctx.Paths.Notifications, opts)

	dims := map[string]any{
		"date":   notif.TopCounts(items, func(n notif.StoredNotification) string { return notif.TsLocalDate(n.Timestamp) }, maxLimit),
		"app":    notif.TopCounts(items, notif.AppLabel, maxLimit),
		"sender": notif.TopCounts(items, notif.SenderLabel, maxLimit),
		"hour":   notif.TopCounts(items, func(n notif.StoredNotification) string { return notif.TsLocalHour(n.Timestamp) }, maxLimit),
		"client": notif.TopCounts(items, notif.ClientLabelOf, maxLimit),
	}
	out := map[string]any{
		"ok": true, "total": len(items),
		"range": map[string]any{"from": from.Raw, "to": to.Raw}, "dim": dim,
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
// 注：聚合原语 TopCounts / AppLabel / SenderLabel / ClientLabelOf / TsLocalDate /
// TsLocalHour / ParseStatsBoundary 已下沉到 internal/notif（CLI 与 yclib 共用，
// 见 arc-cli-library-integration §6 单一真相源）。

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

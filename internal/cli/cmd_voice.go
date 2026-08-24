package cli

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/output"
	"github.com/YoooClaw/cli/internal/voice"
	"github.com/spf13/cobra"
)

const (
	defaultUnfilteredVoiceWindow = 72 * time.Hour
	defaultVoiceRangeNotice      = "未提供任何筛选条件，已默认返回最近 3 天（72 小时）的语音输入历史；如需全部历史，请使用 --all。"
	defaultVoiceAppsWindow       = 7 * 24 * time.Hour
	defaultVoiceAppsRangeNotice  = "未提供时间范围，已默认返回最近 7 天出现过的 App；如需全部历史，请使用 --all。"
)

func newVoiceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "voice",
		Short: "查询本机语音输入历史 🟢",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "按时间倒序列出语音输入历史 🟢",
		Args:  cobra.NoArgs,
		RunE:  run(voiceList),
	}
	addVoiceHistoryFlags(list)
	list.Flags().Bool("all", false, "无其他筛选条件时明确查询全部已保存历史")

	search := &cobra.Command{
		Use:   "search <keyword>",
		Short: "搜索语音输入最终上屏文本 🟢",
		Args:  cobra.ExactArgs(1),
		RunE:  run(voiceSearch),
	}
	addVoiceHistoryFlags(search)

	show := &cobra.Command{
		Use:   "show <voice_id>",
		Short: "查看一条完整语音输入历史 🟢",
		Args:  cobra.ExactArgs(1),
		RunE:  run(voiceShow),
	}

	apps := &cobra.Command{
		Use:   "apps",
		Short: "列出最近使用过的 App ID 和名称 🟢",
		Args:  cobra.NoArgs,
		RunE:  run(voiceApps),
	}
	apps.Flags().String("from", "", "开始时间（含，带时区 ISO 8601 或 YYYY-MM-DD）")
	apps.Flags().String("to", "", "结束时间（不含，带时区 ISO 8601 或 YYYY-MM-DD）")
	apps.Flags().Bool("all", false, "明确扫描全部历史中的 App")

	latest := &cobra.Command{
		Use:   "+latest",
		Short: "查看全历史中最新一条语音输入",
		Args:  cobra.NoArgs,
		RunE:  run(voiceLatest),
	}

	today := &cobra.Command{
		Use:   "+today",
		Short: "列出本地自然日内的语音输入",
		Args:  cobra.NoArgs,
		RunE:  run(voiceToday),
	}
	today.Flags().String("app", "", "按完整 app_id 或 app_name 过滤")

	storagePath := &cobra.Command{
		Use:   "storage-path",
		Short: "打印语音输入 JSONL 与产物目录路径 🟢",
		Args:  cobra.NoArgs,
		RunE:  run(voiceStoragePath),
	}

	c.AddCommand(list, search, show, apps, latest, today, storagePath)
	return c
}

func addVoiceHistoryFlags(cmd *cobra.Command) {
	cmd.Flags().String("from", "", "开始时间（含，带时区 ISO 8601 或 YYYY-MM-DD）")
	cmd.Flags().String("to", "", "结束时间（不含，带时区 ISO 8601 或 YYYY-MM-DD）")
	cmd.Flags().String("app", "", "按完整 app_id 或 app_name 过滤")
	cmd.Flags().String("status", "", "按输入结果状态过滤")
	cmd.Flags().String("language", "", "按识别语言过滤")
	cmd.Flags().Bool("has-audio", false, "只返回当前确有本地音频文件的历史")
	cmd.Flags().Int("limit", 0, "最多返回条数；省略时不限制")
}

func voiceQueryFromFlags(cmd *cobra.Command) (voice.Query, error) {
	from, err := voice.ParseBoundary(flagStr(cmd, "from"), "--from")
	if err != nil {
		return voice.Query{}, err
	}
	to, err := voice.ParseBoundary(flagStr(cmd, "to"), "--to")
	if err != nil {
		return voice.Query{}, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return voice.Query{}, errs.New(errs.CodeInvalidArgument, "--from 必须早于 --to")
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if cmd.Flags().Changed("limit") && limit <= 0 {
		return voice.Query{}, errs.New(errs.CodeInvalidArgument, "--limit 必须是大于 0 的整数")
	}
	return voice.Query{
		From: from, To: to,
		App:      strings.TrimSpace(flagStr(cmd, "app")),
		Status:   strings.TrimSpace(flagStr(cmd, "status")),
		Language: strings.TrimSpace(flagStr(cmd, "language")),
		HasAudio: flagBool(cmd, "has-audio"), Limit: limit,
	}, nil
}

func voiceRange(from, to *time.Time) map[string]any {
	var fromValue, toValue any
	if from != nil {
		fromValue = from.Format(time.RFC3339Nano)
	}
	if to != nil {
		toValue = to.Format(time.RFC3339Nano)
	}
	return map[string]any{"from": fromValue, "to": toValue}
}

func voiceHistoryRows(items []voice.HistoryItem) []any {
	rows := make([]any, len(items))
	for i := range items {
		rows[i] = voiceHistoryRow(items[i])
	}
	return rows
}

func voiceHistoryRow(item voice.HistoryItem) map[string]any {
	return map[string]any{
		"id": item.ID, "started_at": item.StartedAt, "ended_at": item.EndedAt,
		"timezone_offset_min": item.TimezoneOffsetMin, "duration_ms": item.DurationMS,
		"platform": item.Platform, "app_name": item.AppName,
		"window_title": stringPointerValue(item.WindowTitle), "text": item.Text,
		"language": stringPointerValue(item.Language), "char_count": item.CharCount,
		"result_status": item.ResultStatus, "has_audio": item.HasAudio,
		"audio_path": stringPointerValue(item.AudioPath),
	}
}

func stringPointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func queryVoiceHistory(ctx *clictx.Context, cmd *cobra.Command, opts voice.Query) ([]voice.HistoryItem, error) {
	repository, err := voice.Open(cmd.Context(), ctx.Paths.Voice)
	if err != nil {
		return nil, err
	}
	defer repository.Close()
	return repository.List(cmd.Context(), opts)
}

func hasVoiceSelectionFilter(opts voice.Query) bool {
	return opts.From != nil || opts.To != nil || opts.App != "" || opts.Status != "" ||
		opts.Language != "" || opts.HasAudio
}

func voiceList(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	opts, err := voiceQueryFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	defaultRangeApplied := false
	if !flagBool(cmd, "all") && !hasVoiceSelectionFilter(opts) {
		to := voiceQueryNow().In(time.Local)
		from := to.Add(-defaultUnfilteredVoiceWindow)
		opts.From = &from
		opts.To = &to
		defaultRangeApplied = true
	}
	items, err := queryVoiceHistory(ctx, cmd, opts)
	if err != nil {
		return nil, err
	}
	rows := voiceHistoryRows(items)
	if ctx.Format == output.NDJSON {
		return rows, nil
	}
	result := map[string]any{
		"ok": true, "range": voiceRange(opts.From, opts.To),
		"total": len(items), "items": rows,
	}
	if defaultRangeApplied {
		result["default_range_applied"] = true
		result["notice"] = defaultVoiceRangeNotice
	}
	return result, nil
}

func voiceSearch(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	keyword := strings.TrimSpace(args[0])
	if keyword == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "搜索关键词不能为空")
	}
	opts, err := voiceQueryFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	opts.Keyword = keyword
	items, err := queryVoiceHistory(ctx, cmd, opts)
	if err != nil {
		return nil, err
	}
	rows := voiceHistoryRows(items)
	if ctx.Format == output.NDJSON {
		return rows, nil
	}
	return map[string]any{
		"ok": true, "keyword": keyword, "range": voiceRange(opts.From, opts.To),
		"total": len(items), "items": rows,
	}, nil
}

func voiceShow(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	voiceID := strings.TrimSpace(args[0])
	if voiceID == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "voice_id 不能为空")
	}
	repository, err := voice.Open(cmd.Context(), ctx.Paths.Voice)
	if err != nil {
		return nil, err
	}
	defer repository.Close()
	item, err := repository.Show(cmd.Context(), voiceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "voice": voiceHistoryRow(*item)}, nil
}

func voiceApps(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	from, err := voice.ParseBoundary(flagStr(cmd, "from"), "--from")
	if err != nil {
		return nil, err
	}
	to, err := voice.ParseBoundary(flagStr(cmd, "to"), "--to")
	if err != nil {
		return nil, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, errs.New(errs.CodeInvalidArgument, "--from 必须早于 --to")
	}
	defaultRangeApplied := false
	if !flagBool(cmd, "all") && from == nil && to == nil {
		toValue := voiceQueryNow().In(time.Local)
		fromValue := toValue.Add(-defaultVoiceAppsWindow)
		from = &fromValue
		to = &toValue
		defaultRangeApplied = true
	}
	repository, err := voice.Open(cmd.Context(), ctx.Paths.Voice)
	if err != nil {
		return nil, err
	}
	defer repository.Close()
	apps, err := repository.Apps(cmd.Context(), voice.Query{From: from, To: to})
	if err != nil {
		return nil, err
	}
	rows := make([]any, len(apps))
	for i := range apps {
		rows[i] = map[string]any{
			"app_id": apps[i].AppID, "app_name": apps[i].AppName, "history_count": apps[i].HistoryCount,
			"latest_at": apps[i].LatestAt,
		}
	}
	if ctx.Format == output.NDJSON {
		return rows, nil
	}
	result := map[string]any{
		"ok": true, "range": voiceRange(from, to), "total": len(apps), "apps": rows,
	}
	if defaultRangeApplied {
		result["default_range_applied"] = true
		result["notice"] = defaultVoiceAppsRangeNotice
	}
	return result, nil
}

func voiceLatest(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	items, err := queryVoiceHistory(ctx, cmd, voice.Query{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return map[string]any{"ok": true, "voice": nil}, nil
	}
	return map[string]any{"ok": true, "voice": voiceHistoryRow(items[0])}, nil
}

var voiceQueryNow = time.Now

func voiceToday(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	now := voiceQueryNow().In(time.Local)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 1)
	opts := voice.Query{From: &from, To: &to, App: strings.TrimSpace(flagStr(cmd, "app"))}
	items, err := queryVoiceHistory(ctx, cmd, opts)
	if err != nil {
		return nil, err
	}
	rows := voiceHistoryRows(items)
	if ctx.Format == output.NDJSON {
		return rows, nil
	}
	return map[string]any{
		"ok": true, "date": from.Format("2006-01-02"), "range": voiceRange(&from, &to),
		"total": len(items), "items": rows,
	}, nil
}

func voiceStoragePath(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return map[string]any{
		"ok": true, "path": ctx.Paths.Voice,
		"history":            filepath.Join(ctx.Paths.Voice, "audio-jsonl"),
		"audio":              filepath.Join(ctx.Paths.Voice, "audio"),
		"recordings_history": filepath.Join(ctx.Paths.Voice, "recordings-jsonl"),
		"recordings":         filepath.Join(ctx.Paths.Voice, "recordings"),
		"format":             "daily-jsonl",
	}, nil
}

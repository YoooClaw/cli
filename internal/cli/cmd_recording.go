package cli

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/capturerecording"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/spf13/cobra"
)

func newRecordingCmd() *cobra.Command {
	c := &cobra.Command{Use: "recording", Short: "录音查询与 ASR 配置 🟢"}

	list := &cobra.Command{Use: "list", Short: "列出所有录音 🟢", Args: cobra.NoArgs, RunE: run(recordingList)}
	list.Flags().String("status", "", "按录音来源状态过滤")
	list.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	list.Flags().String("from", "", "录音时间起点（含，ISO 8601 或 YYYY-MM-DD）")
	list.Flags().String("to", "", "录音时间终点（不含，ISO 8601 或 YYYY-MM-DD）")
	list.Flags().String("source", "all", "录音来源：all | capture_app | smart_hardware")
	status := &cobra.Command{Use: "status <id>", Short: "查看单条录音详情 🟢", Args: cobra.ExactArgs(1), RunE: run(recordingStatusCmd)}
	status.Flags().String("source", "all", "录音来源：all | capture_app | smart_hardware")
	storagePath := &cobra.Command{Use: "storage-path", Short: "打印录音存储目录绝对路径 🟢", Args: cobra.NoArgs, RunE: run(recordingStoragePath)}

	setupAsr := &cobra.Command{Use: "setup-asr", Short: "交互式配置 ASR 转写参数 🟢", Args: cobra.NoArgs, RunE: run(recordingSetupAsr)}
	setupAsr.Flags().String("mode", "", "api | local（默认 api）")
	setupAsr.Flags().String("api-key", "", "api 模式：可选；留空则用 account ock- key")
	setupAsr.Flags().String("endpoint", "", "api 模式：自定义 model-proxy 端点")
	setupAsr.Flags().String("language", "", "api 模式：语言提示，如 auto / zh / zh-CN / zh-TW / zh-Hant / en")
	setupAsr.Flags().String("model", "", "local 模式：Whisper 模型名")
	setupAsr.Flags().Bool("non-interactive", false, "跳过向导，从参数构造配置")

	events := &cobra.Command{Use: "events", Short: "查询录音状态事件流（JSONL）🟢", Args: cobra.NoArgs, RunE: run(recordingEvents)}
	events.Flags().String("id", "", "只看某条录音的事件")
	events.Flags().String("since", "", "回看时间窗，如 10m / 1h / 24h")
	events.Flags().Bool("watch", false, "持续跟随新事件（本 build 暂返回快照）")
	events.Flags().String("limit", "200", "返回条数上限")

	latest := &cobra.Command{Use: "+latest", Short: "展示最新一条录音详情", Args: cobra.NoArgs, RunE: run(recordingLatest)}
	latest.Flags().String("source", "all", "录音来源：all | capture_app | smart_hardware")
	today := &cobra.Command{Use: "+today", Short: "列出本地自然日内的今日录音", Args: cobra.NoArgs, RunE: run(recordingToday)}
	today.Flags().String("status", "", "按录音来源状态过滤")
	today.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	today.Flags().String("source", "all", "录音来源：all | capture_app | smart_hardware")

	c.AddCommand(list, status, storagePath, setupAsr, events, latest, today)
	return c
}

func recToListItem(r recording.Entry) map[string]any {
	return map[string]any{
		"id": r.ID, "source_type": recordingSourceSmartHardware,
		"source_name": recordingSourceSmartHardwareName,
		"clientLabel": labelOrLegacy2(r.ClientLabel), "name": r.Metadata.Name,
		"title":        nilIfEmpty(r.Title),
		"duration_ms":  int64(r.Metadata.DurationSec * 1000),
		"duration_sec": r.Metadata.DurationSec, "duration_display": recording.FormatDurationDisplay(r.Metadata.DurationSec),
		"status":            r.Status,
		"file_size_display": firstNonEmpty(r.Metadata.FileSizeDisplay, "--"),
		"audio_status":      r.AudioStatus,
		"has_audio":         r.AudioFile != "", "has_transcript": r.TranscriptFile != "", "has_summary": r.SummaryFile != "",
		"missing_artifacts": []string{},
		"created_at":        r.Metadata.CreatedAt, "updated_at": r.UpdatedAt, "error": nilIfEmpty(r.LastError),
	}
}

const (
	recordingSourceAll               = "all"
	recordingSourceSmartHardware     = "smart_hardware"
	recordingSourceSmartHardwareName = "YoooClaw 智能硬件"
)

type recordingQueryItem struct {
	id         string
	sourceType string
	occurredAt time.Time
	hasTime    bool
	list       map[string]any
	detail     map[string]any
}

func parseRecordingSource(raw string) (string, error) {
	source := strings.ToLower(strings.TrimSpace(raw))
	if source == "" {
		source = recordingSourceAll
	}
	switch source {
	case recordingSourceAll, capturerecording.SourceType, recordingSourceSmartHardware:
		return source, nil
	default:
		return "", errs.Newf(errs.CodeInvalidArgument,
			"--source 必须是 all、capture_app 或 smart_hardware（收到 %q）", raw)
	}
}

func captureToListItem(item capturerecording.Item) map[string]any {
	return map[string]any{
		"id": item.ID, "source_type": capturerecording.SourceType,
		"source_name": capturerecording.SourceName,
		"clientLabel": nil, "name": item.Title, "title": item.Title,
		"duration_ms": item.DurationMS, "duration_sec": float64(item.DurationMS) / 1000,
		"duration_display": recording.FormatDurationDisplay(float64(item.DurationMS) / 1000),
		"status":           item.Status, "file_size_display": "--", "audio_status": nil,
		"has_audio": item.HasAudio, "has_transcript": item.HasTranscript, "has_summary": item.HasSummary,
		"recorded_at": item.RecordedAt, "created_at": item.RecordedAt, "updated_at": nil, "error": nil,
		"missing_artifacts": item.MissingArtifacts, "diagnostics": item.Diagnostics,
	}
}

func captureDetail(item capturerecording.Item) map[string]any {
	detail := captureToListItem(item)
	detail["audioFile"] = stringPointerValue(item.AudioRelPath)
	detail["transcriptFile"] = stringPointerValue(item.TranscriptRelPath)
	detail["summaryFile"] = stringPointerValue(item.SummaryRelPath)
	detail["audio_path"] = stringPointerValue(item.AudioPath)
	detail["transcript_path"] = stringPointerValue(item.TranscriptPath)
	detail["summary_path"] = stringPointerValue(item.SummaryPath)
	return detail
}

func recordingList(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	from, err := parseRecordingBoundary(flagStr(cmd, "from"), "--from")
	if err != nil {
		return nil, err
	}
	to, err := parseRecordingBoundary(flagStr(cmd, "to"), "--to")
	if err != nil {
		return nil, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, errs.New(errs.CodeInvalidArgument, "--from 必须早于 --to")
	}
	return recordingListRange(cmd.Context(), ctx, flagStr(cmd, "status"), flagStr(cmd, "client"), flagStr(cmd, "source"), from, to)
}

func queryRecordings(queryCtx context.Context, ctx *clictx.Context, status, client, source string, from, to *time.Time) ([]recordingQueryItem, error) {
	source, err := parseRecordingSource(source)
	if err != nil {
		return nil, err
	}
	items := make([]recordingQueryItem, 0)
	if source == recordingSourceAll || source == recordingSourceSmartHardware {
		for _, r := range recording.ReadIndex(ctx.Paths.Recordings) {
			if status != "" && r.Status != status {
				continue
			}
			if client != "" && client != "all" && labelOrLegacy2(r.ClientLabel) != client {
				continue
			}
			occurredAt, hasTime := recording.EffectiveTime(r)
			if (from != nil || to != nil) && !hasTime {
				continue
			}
			if from != nil && occurredAt.Before(*from) || to != nil && !occurredAt.Before(*to) {
				continue
			}
			items = append(items, recordingQueryItem{
				id: r.ID, sourceType: recordingSourceSmartHardware,
				occurredAt: occurredAt, hasTime: hasTime,
				list: recToListItem(r), detail: recordingDetail(r),
			})
		}
	}
	if (source == recordingSourceAll || source == capturerecording.SourceType) &&
		(client == "" || client == "all") {
		captureItems, err := capturerecording.List(queryCtx, ctx.Paths.Voice, capturerecording.Query{
			From: from, To: to, Status: status,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range captureItems {
			items = append(items, recordingQueryItem{
				id: item.ID, sourceType: capturerecording.SourceType,
				occurredAt: item.RecordedTime, hasTime: true,
				list: captureToListItem(item), detail: captureDetail(item),
			})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].hasTime != items[j].hasTime {
			return items[i].hasTime
		}
		if !items[i].occurredAt.Equal(items[j].occurredAt) {
			return items[i].occurredAt.After(items[j].occurredAt)
		}
		if items[i].id != items[j].id {
			return items[i].id > items[j].id
		}
		return items[i].sourceType < items[j].sourceType
	})
	return items, nil
}

func recordingListRange(queryCtx context.Context, ctx *clictx.Context, status, client, source string, from, to *time.Time) (map[string]any, error) {
	source, err := parseRecordingSource(source)
	if err != nil {
		return nil, err
	}
	items, err := queryRecordings(queryCtx, ctx, status, client, source, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i].list
	}
	return map[string]any{"ok": true, "source": source, "total": len(out), "recordings": out}, nil
}

func parseRecordingBoundary(raw, flagName string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, ok := recording.ParseTime(raw)
	if !ok {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"%s 必须是 ISO 8601 时间或 YYYY-MM-DD（收到 %q）", flagName, raw)
	}
	return &parsed, nil
}

func recordingDetail(r recording.Entry) map[string]any {
	var markers any = r.Metadata.Markers
	if markers == nil {
		markers = []any{}
	}
	return map[string]any{
		"id": r.ID, "source_type": recordingSourceSmartHardware,
		"source_name": recordingSourceSmartHardwareName,
		"clientLabel": labelOrLegacy2(r.ClientLabel), "name": r.Metadata.Name,
		"recorded_at":       r.Metadata.CreatedAt,
		"duration_ms":       int64(r.Metadata.DurationSec * 1000),
		"duration_sec":      r.Metadata.DurationSec,
		"duration_display":  recording.FormatDurationDisplay(r.Metadata.DurationSec),
		"file_size_display": firstNonEmpty(r.Metadata.FileSizeDisplay, "--"),
		"status":            r.Status, "created_at": r.Metadata.CreatedAt, "location": r.Metadata.Location,
		"audio_status": r.AudioStatus,
		"has_audio":    r.AudioFile != "", "has_transcript": r.TranscriptFile != "", "has_summary": r.SummaryFile != "",
		"missing_artifacts": []string{},
		"markers":           markers, "audioFile": nilIfEmpty(r.AudioFile), "srtFile": nilIfEmpty(r.SrtFile),
		"transcriptDataFile": nilIfEmpty(r.TranscriptDataFile), "transcriptFile": nilIfEmpty(r.TranscriptFile),
		"summaryFile": nilIfEmpty(r.SummaryFile), "title": nilIfEmpty(r.Title), "error": nilIfEmpty(r.LastError),
		"ingestedAt": r.IngestedAt, "updatedAt": r.UpdatedAt,
	}
}

func recordingStatusCmd(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	source, err := parseRecordingSource(flagStr(cmd, "source"))
	if err != nil {
		return nil, err
	}
	items, err := queryRecordings(cmd.Context(), ctx, "", "", source, nil, nil)
	if err != nil {
		return nil, err
	}
	matches := make([]recordingQueryItem, 0, 2)
	for _, item := range items {
		if item.id == args[0] {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return nil, errs.New(errs.CodeNotFound, "录音不存在："+args[0])
	}
	if len(matches) > 1 && source == recordingSourceAll {
		sources := make([]string, len(matches))
		for i := range matches {
			sources[i] = matches[i].sourceType
		}
		return nil, errs.New(errs.CodeInvalidArgument, "录音 ID 在多个来源中重复，请使用 --source 指定来源",
			map[string]any{"id": args[0], "sources": sources})
	}
	return map[string]any{"ok": true, "recording": matches[0].detail}, nil
}

func recordingStoragePath(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return map[string]any{
		"ok": true, "path": ctx.Paths.Recordings,
		"sources": map[string]any{
			recordingSourceSmartHardware: map[string]any{
				"path": ctx.Paths.Recordings, "index": filepath.Join(ctx.Paths.Recordings, "index.json"),
			},
			capturerecording.SourceType: map[string]any{
				"path":    capturerecording.RecordingsPath(ctx.Paths.Voice),
				"history": capturerecording.HistoryPath(ctx.Paths.Voice),
			},
		},
	}, nil
}

func recordingLatest(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	source, err := parseRecordingSource(flagStr(cmd, "source"))
	if err != nil {
		return nil, err
	}
	items, err := queryRecordings(cmd.Context(), ctx, "", "", source, nil, nil)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return map[string]any{"ok": true, "recording": nil}, nil
	}
	return map[string]any{"ok": true, "recording": items[0].detail}, nil
}

var recordingQueryNow = time.Now

func recordingToday(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	now := recordingQueryNow().In(time.Local)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 1)
	result, err := recordingListRange(cmd.Context(), ctx, flagStr(cmd, "status"), flagStr(cmd, "client"), flagStr(cmd, "source"), &from, &to)
	if err != nil {
		return nil, err
	}
	result["date"] = from.Format("2006-01-02")
	result["from"] = from.Format(time.RFC3339)
	result["to"] = to.Format(time.RFC3339)
	return result, nil
}

var sinceRE = regexp.MustCompile(`^(\d+)([smhd])$`)

func parseSince(input string) (*time.Time, error) {
	if input == "" {
		return nil, nil
	}
	m := sinceRE.FindStringSubmatch(strings.TrimSpace(input))
	if m == nil {
		return nil, errs.Newf(errs.CodeInvalidArgument, `--since 格式应为 <数字><单位>，单位 s/m/h/d（收到 "%s"）`, input)
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	t := time.Now().Add(-time.Duration(n) * unit)
	return &t, nil
}

func recordingEvents(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	path := filepath.Join(ctx.Paths.Recordings, "state", "events.jsonl")
	limit := atoiDefault(flagStr(cmd, "limit"), 200)
	since, err := parseSince(flagStr(cmd, "since"))
	if err != nil {
		return nil, err
	}
	id := flagStr(cmd, "id")
	events := recording.ReadEvents(path)
	filtered := make([]any, 0, len(events))
	for _, e := range events {
		if id != "" && e["recordingId"] != id {
			continue
		}
		if since != nil {
			tsStr, _ := e["ts"].(string)
			if ts, ok := parseEventTime(tsStr); !ok || ts.Before(*since) {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return map[string]any{"ok": true, "path": path, "total": len(filtered), "events": filtered}, nil
}

func parseEventTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func recordingSetupAsr(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	mode := strings.ToLower(firstNonEmpty(flagStr(cmd, "mode"), "api"))
	apiKey := flagStr(cmd, "api-key")
	endpoint := flagStr(cmd, "endpoint")
	language := flagStr(cmd, "language")
	model := flagStr(cmd, "model")

	if !flagBool(cmd, "non-interactive") && prompt.IsInteractive() {
		var err error
		if mode, err = prompt.Ask("ASR mode（api / local）", mode); err != nil {
			return nil, err
		}
		mode = strings.ToLower(mode)
		if mode == "api" {
			if apiKey, err = prompt.Ask("API key（留空则使用 account ock- key）", apiKey); err != nil {
				return nil, err
			}
			if endpoint, err = prompt.Ask("model-proxy endpoint（留空走默认）", endpoint); err != nil {
				return nil, err
			}
			if language, err = prompt.Ask("语言提示（auto / zh / zh-CN / zh-TW / zh-Hant / en）", firstNonEmpty(language, "auto")); err != nil {
				return nil, err
			}
		} else if mode == "local" {
			if model, err = prompt.Ask("Whisper 模型（留空走推荐值）", model); err != nil {
				return nil, err
			}
		}
	}
	if mode != "api" && mode != "local" {
		return nil, errs.Newf(errs.CodeInvalidArgument, `--mode 必须是 api 或 local（收到 "%s"）`, mode)
	}

	cfg := buildAsrConfig(mode, apiKey, endpoint, language, model)
	if err := fsutil.EnsureDir(ctx.Paths.Recordings, fsutil.DirMode); err != nil {
		return nil, err
	}
	path := filepath.Join(ctx.Paths.Recordings, "asr-config.json")
	if err := fsutil.WriteJSON(path, cfg, fsutil.ConfigFileMode); err != nil {
		return nil, err
	}
	var keyConfigured any = "n/a"
	if mode == "api" {
		if apiKey != "" {
			keyConfigured = true
		} else {
			keyConfigured = "fallback-to-account"
		}
	}
	return map[string]any{"ok": true, "path": path, "mode": mode, "keyConfigured": keyConfigured}, nil
}

func buildAsrConfig(mode, apiKey, endpoint, language, model string) map[string]any {
	if mode == "api" {
		api := map[string]any{}
		if apiKey != "" {
			api["apiKey"] = apiKey
		}
		if endpoint != "" {
			api["endpoint"] = endpoint
		}
		if language != "" {
			api["language"] = language
		}
		if len(api) > 0 {
			return map[string]any{"mode": mode, "api": api}
		}
		return map[string]any{"mode": mode}
	}
	local := map[string]any{}
	if model != "" {
		local["model"] = model
	}
	if len(local) > 0 {
		return map[string]any{"mode": mode, "local": local}
	}
	return map[string]any{"mode": mode}
}

func labelOrLegacy2(label string) string {
	if label == "" {
		return "legacy"
	}
	return label
}

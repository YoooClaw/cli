package cli

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	list.Flags().String("status", "", "按传输状态过滤")
	list.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	status := &cobra.Command{Use: "status <id>", Short: "查看单条录音详情 🟢", Args: cobra.ExactArgs(1), RunE: run(recordingStatusCmd)}
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

	c.AddCommand(list, status, storagePath, setupAsr, events, latest)
	return c
}

func recToListItem(r recording.Entry) map[string]any {
	return map[string]any{
		"id": r.ID, "clientLabel": labelOrLegacy2(r.ClientLabel), "name": r.Metadata.Name,
		"duration_sec": r.Metadata.DurationSec, "status": r.Status, "file_size_bytes": r.Metadata.FileSizeBytes,
		"has_audio": r.AudioFile != "", "has_transcript": r.TranscriptFile != "",
		"created_at": r.Metadata.CreatedAt, "updated_at": r.UpdatedAt, "error": nilIfEmpty(r.LastError),
	}
}

func recordingList(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	recs := recording.ReadIndex(ctx.Paths.Recordings)
	status := flagStr(cmd, "status")
	client := flagStr(cmd, "client")
	out := make([]any, 0, len(recs))
	for _, r := range recs {
		if status != "" && r.Status != status {
			continue
		}
		if client != "" && client != "all" && labelOrLegacy2(r.ClientLabel) != client {
			continue
		}
		out = append(out, recToListItem(r))
	}
	return map[string]any{"ok": true, "total": len(out), "recordings": out}, nil
}

func findRecording(ctx *clictx.Context, id string) (recording.Entry, bool) {
	for _, r := range recording.ReadIndex(ctx.Paths.Recordings) {
		if r.ID == id {
			return r, true
		}
	}
	return recording.Entry{}, false
}

func recordingDetail(r recording.Entry) map[string]any {
	var markers any = r.Metadata.Markers
	if markers == nil {
		markers = []any{}
	}
	return map[string]any{
		"id": r.ID, "clientLabel": labelOrLegacy2(r.ClientLabel), "name": r.Metadata.Name,
		"duration_sec": r.Metadata.DurationSec, "file_size_bytes": r.Metadata.FileSizeBytes,
		"status": r.Status, "created_at": r.Metadata.CreatedAt, "location": r.Metadata.Location,
		"markers": markers, "audioFile": nilIfEmpty(r.AudioFile), "srtFile": nilIfEmpty(r.SrtFile),
		"transcriptDataFile": nilIfEmpty(r.TranscriptDataFile), "transcriptFile": nilIfEmpty(r.TranscriptFile),
		"summaryFile": nilIfEmpty(r.SummaryFile), "title": nilIfEmpty(r.Title), "error": nilIfEmpty(r.LastError),
		"ingestedAt": r.IngestedAt, "updatedAt": r.UpdatedAt,
	}
}

func recordingStatusCmd(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	r, ok := findRecording(ctx, args[0])
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "录音不存在："+args[0])
	}
	return map[string]any{"ok": true, "recording": recordingDetail(r)}, nil
}

func recordingStoragePath(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return map[string]any{"ok": true, "path": ctx.Paths.Recordings}, nil
}

func recordingLatest(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	recs := recording.ReadIndex(ctx.Paths.Recordings)
	if len(recs) == 0 {
		return map[string]any{"ok": true, "recording": nil}, nil
	}
	recording.SortByCreatedDesc(recs)
	return map[string]any{"ok": true, "recording": recordingDetail(recs[0])}, nil
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

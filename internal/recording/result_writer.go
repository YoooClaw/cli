package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// ResultTranscriptSource 是 result.write 入参里转写的上游来源。
type ResultTranscriptSource struct {
	Provider  string `json:"provider,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Status    string `json:"status,omitempty"`
}

// ResultTranscriptSegment 是 result.write 入参里的转写分段。
type ResultTranscriptSegment struct {
	Text      string   `json:"text"`
	StartMS   *float64 `json:"startMs,omitempty"`
	EndMS     *float64 `json:"endMs,omitempty"`
	SpeakerID *int     `json:"speakerId,omitempty"`
}

// ResultTranscript 是 result.write 写入的转写结果。
type ResultTranscript struct {
	GeneratedAt string                    `json:"generatedAt,omitempty"`
	Source      *ResultTranscriptSource   `json:"source,omitempty"`
	Title       string                    `json:"title,omitempty"`
	Category    string                    `json:"category,omitempty"`
	Brief       string                    `json:"brief,omitempty"`
	Text        string                    `json:"text,omitempty"`
	Segments    []ResultTranscriptSegment `json:"segments,omitempty"`
	Markdown    string                    `json:"markdown,omitempty"`
}

// ResultSummary 是 result.write 写入的总结结果。
type ResultSummary struct {
	GeneratedAt string          `json:"generatedAt,omitempty"`
	Format      string          `json:"format,omitempty"`
	Markdown    string          `json:"markdown,omitempty"`
	Structured  json.RawMessage `json:"structured,omitempty"`
}

// ResultWriteParams 是 recordings.result.write 的入参。
type ResultWriteParams struct {
	RecordingID string            `json:"recordingId"`
	OssURL      string            `json:"ossUrl,omitempty"`
	Transcript  *ResultTranscript `json:"transcript,omitempty"`
	Summary     *ResultSummary    `json:"summary,omitempty"`
}

// ResultWriteStored 记录 result.write 落盘的相对路径。
type ResultWriteStored struct {
	TranscriptDataFile string `json:"transcriptDataFile,omitempty"`
	TranscriptFile     string `json:"transcriptFile,omitempty"`
	SummaryFile        string `json:"summaryFile,omitempty"`
}

// ResultWriteResult 是 recordings.result.write 的返回。
type ResultWriteResult struct {
	OK             bool              `json:"ok"`
	RecordingID    string            `json:"recordingId"`
	TransferStatus string            `json:"transfer_status"`
	Stored         ResultWriteStored `json:"stored"`
}

// HandleRecordingResultWrite 处理 recordings.result.write：写入 App / 云端生成的
// 转录与总结，覆盖同一录音的 transcript-data/transcripts/summaries 文件。
// 找不到录音时按占位元数据 upsert 新建；
// 可选 ossUrl 时在后台下载并覆盖本地音频。不触发插件侧 ASR。
func HandleRecordingResultWrite(params ResultWriteParams, storage *Storage, logger Logger, opts SyncOptions) (ResultWriteResult, error) {
	recordingID := strings.TrimSpace(params.RecordingID)
	if recordingID == "" {
		return ResultWriteResult{}, errors.New("recordingId is required")
	}
	// 兜底（daemon 入口已校验）：recordingID 会拼进落盘文件名。
	if !fsutil.IsSafeName(recordingID) {
		return ResultWriteResult{}, errors.New("recordingId is invalid")
	}
	if params.Transcript == nil && params.Summary == nil {
		return ResultWriteResult{}, errors.New("transcript or summary is required")
	}

	entry, found := storage.FindByID(recordingID)
	if !found {
		if _, err := storage.Ingest(recordingID, buildPlaceholderMetadata(recordingID, params), ""); err != nil {
			return ResultWriteResult{}, err
		}
		var ok bool
		entry, ok = storage.FindByID(recordingID)
		if !ok {
			return ResultWriteResult{}, fmt.Errorf("Recording not found: %s", recordingID)
		}
		logger.Info("[recording-result] 录音不存在，已按结果写入新建: " + recordingID)
	}

	var transcript []TranscriptItem
	title := firstNonEmpty(entry.Title, entry.Metadata.Name, recordingID)
	var summaryText string

	if params.Transcript != nil {
		t, items, err := writeResultTranscript(recordingID, entry.Metadata, *params.Transcript, storage, logger)
		if err != nil {
			return ResultWriteResult{}, err
		}
		title = t
		transcript = items
	}

	if params.Summary != nil {
		st, err := writeResultSummary(recordingID, *params.Summary, storage, logger)
		if err != nil {
			return ResultWriteResult{}, err
		}
		summaryText = st
	}

	updated, err := storage.MarkResultWritten(recordingID)
	if err != nil {
		return ResultWriteResult{}, err
	}

	if summaryText == "" {
		summaryText = readSummaryFile(storage, recordingID)
	}
	emitResultStatus(recordingID, storage, logger, opts.NotifyStatus, transcript, summaryText, title)

	if oss := strings.TrimSpace(params.OssURL); oss != "" {
		go downloadResultAudio(recordingID, oss, storage, logger, opts)
	}

	return ResultWriteResult{
		OK:             true,
		RecordingID:    recordingID,
		TransferStatus: updated.Status,
		Stored: ResultWriteStored{
			TranscriptDataFile: updated.TranscriptDataFile,
			TranscriptFile:     updated.TranscriptFile,
			SummaryFile:        updated.SummaryFile,
		},
	}, nil
}

// buildPlaceholderMetadata 在找不到录音时，用请求里能拿到的信息拼一份占位元数据。
func buildPlaceholderMetadata(recordingID string, params ResultWriteParams) Metadata {
	name := recordingID
	createdAt := nowISO()
	if params.Transcript != nil {
		if t := strings.TrimSpace(params.Transcript.Title); t != "" {
			name = t
		}
		if g := strings.TrimSpace(params.Transcript.GeneratedAt); g != "" {
			createdAt = g
		}
	}
	return Metadata{
		Name:        name,
		CreatedAt:   createdAt,
		OssAudioURL: strings.TrimSpace(params.OssURL),
		Status:      StatusSynced,
	}
}

func writeResultTranscript(recordingID string, meta Metadata, t ResultTranscript, storage *Storage, logger Logger) (string, []TranscriptItem, error) {
	title := firstNonEmpty(strings.TrimSpace(t.Title), strings.TrimSpace(meta.Name), recordingID)
	segments := normalizeResultSegments(t.Segments)
	text := firstNonEmpty(normalizeMultiline(t.Text), joinSegmentTexts(segments), normalizeMultiline(t.Markdown))

	source := TranscriptSource{Provider: "app", Delivery: "result-write"}
	if t.Source != nil {
		if p := strings.TrimSpace(t.Source.Provider); p != "" {
			source.Provider = p
		}
		source.TaskID = strings.TrimSpace(t.Source.TaskID)
		source.RequestID = strings.TrimSpace(t.Source.RequestID)
		source.Status = strings.TrimSpace(t.Source.Status)
	}

	doc := BuildTranscriptDocument(recordingID, source, title, strings.TrimSpace(t.Category), normalizeMultiline(t.Brief), text, segments, t)
	if g := strings.TrimSpace(t.GeneratedAt); g != "" {
		doc.GeneratedAt = g
	}

	dataBytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", nil, err
	}
	dataName := TranscriptDataFilename(recordingID)
	if err := fsutil.WriteAtomic(filepath.Join(storage.TranscriptDataDir(), dataName), dataBytes, fsutil.ConfigFileMode); err != nil {
		return "", nil, err
	}
	_ = storage.SetTranscriptDataFile(recordingID, dataName)

	markdown := normalizeMultiline(t.Markdown)
	if strings.TrimSpace(markdown) == "" {
		markdown = buildResultTranscriptMarkdown(meta, title, text, segments)
	}
	mdName := TranscriptFilename(recordingID, title)
	if err := fsutil.WriteAtomic(filepath.Join(storage.TranscriptsDir(), mdName), []byte(ensureTrailingNewline(markdown)), fsutil.ConfigFileMode); err != nil {
		return "", nil, err
	}
	_ = storage.SetTranscriptFile(recordingID, mdName)
	_ = storage.SetTitle(recordingID, title)
	logger.Info("[recording-result] 转录文本已写入: " + recordingID)

	return title, ExtractSourceTextListFromDocument(doc), nil
}

func writeResultSummary(recordingID string, sm ResultSummary, storage *Storage, logger Logger) (string, error) {
	markdown := normalizeMultiline(sm.Markdown)
	if strings.TrimSpace(markdown) == "" {
		markdown = buildStructuredSummaryMarkdown(sm.Structured)
	}
	if strings.TrimSpace(markdown) == "" {
		return "", nil
	}
	name := SummaryFilename(recordingID)
	if err := fsutil.WriteAtomic(filepath.Join(storage.SummariesDir(), name), []byte(ensureTrailingNewline(markdown)), fsutil.ConfigFileMode); err != nil {
		return "", err
	}
	_ = storage.SetSummaryFile(recordingID, name)
	logger.Info("[recording-result] 总结已写入: " + recordingID)
	return strings.TrimSpace(markdown), nil
}

func downloadResultAudio(recordingID, ossURL string, storage *Storage, logger Logger, opts SyncOptions) {
	dest := storage.AudioFilePath(recordingID, ossURL)
	logger.Info("[recording-result] 开始下载音频: " + recordingID + ", audio=" + ossURL)
	result := DownloadFile(ossURL, dest, logger, opts.DownloadOptions)
	if !result.OK {
		message := "音频下载失败: " + result.Error
		logger.Error("[recording-result] " + message + ": " + recordingID)
		_ = storage.SetLastError(recordingID, message)
		emitResultDownloadStatus(recordingID, storage, logger, opts.NotifyStatus)
		return
	}
	_ = storage.SetAudioFile(recordingID, AudioFilename(recordingID, ossURL))
	logger.Info("[recording-result] 音频已更新: " + recordingID)
	emitResultDownloadStatus(recordingID, storage, logger, opts.NotifyStatus)
}

func emitResultDownloadStatus(recordingID string, storage *Storage, logger Logger, notify func(StatusEvent)) {
	entry, ok := storage.FindByID(recordingID)
	if !ok {
		return
	}
	title := firstNonEmpty(entry.Title, entry.Metadata.Name, recordingID)
	emitResultStatus(recordingID, storage, logger, notify, nil, readSummaryFile(storage, recordingID), title)
}

func emitResultStatus(recordingID string, storage *Storage, logger Logger, notify func(StatusEvent), transcript []TranscriptItem, summary, title string) {
	if notify == nil {
		return
	}
	entry, ok := storage.FindByID(recordingID)
	if !ok {
		return
	}
	if title == "" {
		title = firstNonEmpty(entry.Title, entry.Metadata.Name, recordingID)
	}
	event := StatusEvent{
		RecordingID:        entry.ID,
		TransferStatus:     entry.Status,
		AudioFile:          entry.AudioFile,
		SrtFile:            entry.SrtFile,
		TranscriptDataFile: entry.TranscriptDataFile,
		TranscriptFile:     entry.TranscriptFile,
		SummaryFile:        entry.SummaryFile,
		Transcript:         transcript,
		Summary:            summary,
		Title:              title,
		UpdatedAt:          entry.UpdatedAt,
		Error:              entry.LastError,
	}
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error(fmt.Sprintf("[recording-result] 状态事件发送失败: %s, %v", recordingID, rec))
		}
	}()
	notify(event)
}

func normalizeResultSegments(segments []ResultTranscriptSegment) []TranscriptSegment {
	out := make([]TranscriptSegment, 0, len(segments))
	for _, seg := range segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		out = append(out, TranscriptSegment{
			Text:      text,
			StartMS:   seg.StartMS,
			EndMS:     seg.EndMS,
			SpeakerID: seg.SpeakerID,
		})
	}
	return out
}

func joinSegmentTexts(segments []TranscriptSegment) string {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		if t := strings.TrimSpace(seg.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func buildResultTranscriptMarkdown(meta Metadata, title, text string, segments []TranscriptSegment) string {
	var lines []string
	lines = append(lines, "# "+title, "")
	lines = append(lines, "> 录音名称："+meta.Name)
	lines = append(lines, "> 时长："+formatDuration(meta.DurationSec))
	lines = append(lines, "> 创建时间："+meta.CreatedAt)
	if len(meta.Markers) > 0 {
		lines = append(lines, fmt.Sprintf("> 关键点数：%d", len(meta.Markers)))
	}
	lines = append(lines, "", "---", "")

	if len(segments) > 0 {
		for _, seg := range segments {
			prefix := ""
			if seg.StartMS != nil {
				prefix = "[" + formatTimestamp(*seg.StartMS) + "] "
			}
			lines = append(lines, strings.TrimSpace(prefix+seg.Text), "")
		}
	} else {
		lines = append(lines, text, "")
	}

	return strings.Join(lines, "\n")
}

func buildStructuredSummaryMarkdown(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		lines := []string{"# 总结", ""}
		appendSummarySection(&lines, "结论", val["decisions"])
		appendSummarySection(&lines, "待办", val["todos"])
		appendSummarySection(&lines, "风险", val["risks"])
		appendSummarySection(&lines, "未决问题", val["openQuestions"])
		if len(lines) == 2 {
			pretty, _ := json.MarshalIndent(v, "", "  ")
			lines = append(lines, "```json", string(pretty), "```")
		}
		return strings.Join(lines, "\n")
	default:
		return fmt.Sprintf("%v", val)
	}
}

func appendSummarySection(lines *[]string, title string, value any) {
	arr, ok := value.([]any)
	if !ok || len(arr) == 0 {
		return
	}
	*lines = append(*lines, "## "+title, "")
	for _, item := range arr {
		if str, ok := item.(string); ok {
			*lines = append(*lines, "- "+str)
			continue
		}
		b, _ := json.Marshal(item)
		*lines = append(*lines, "- "+string(b))
	}
	*lines = append(*lines, "")
}

func readSummaryFile(storage *Storage, recordingID string) string {
	data, err := os.ReadFile(filepath.Join(storage.SummariesDir(), SummaryFilename(recordingID)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func normalizeMultiline(v string) string {
	return strings.ReplaceAll(v, "\r\n", "\n")
}

func ensureTrailingNewline(v string) string {
	if strings.HasSuffix(v, "\n") {
		return v
	}
	return v + "\n"
}

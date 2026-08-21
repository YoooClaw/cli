// Package capturerecording 只读查询 YoooClaw Capture 写入的会议录音每日索引。
package capturerecording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	SourceType = "capture_app"
	SourceName = "YoooClaw Capture"

	historyDirName    = "recordings-jsonl"
	recordingsDirName = "recordings"
)

var dailyIndexName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)

// Query 是 Capture 会议录音的索引查询条件。From 包含，To 不包含。
type Query struct {
	From   *time.Time
	To     *time.Time
	Status string
}

// Item 是一条 Capture 会议录音索引及当前产物事实。status 仅原样透传，
// has_* 只由安全路径下的真实普通文件决定。
type Item struct {
	ID                string
	Title             string
	AudioRelPath      *string
	TranscriptRelPath *string
	SummaryRelPath    *string
	RecordedAt        string
	DurationMS        int64
	Status            string
	HasAudio          bool
	HasTranscript     bool
	HasSummary        bool
	AudioPath         *string
	TranscriptPath    *string
	SummaryPath       *string
	MissingArtifacts  []string
	Diagnostics       []string

	RecordedTime time.Time
}

type sourceItem struct {
	ID                string  `json:"id"`
	Title             string  `json:"title"`
	AudioRelPath      *string `json:"audio_rel_path"`
	TranscriptRelPath *string `json:"transcript_rel_path"`
	SummaryRelPath    *string `json:"summary_rel_path"`
	RecordedAt        string  `json:"recorded_at"`
	DurationMS        int64   `json:"duration_ms"`
	Status            string  `json:"status"`

	recordedTime time.Time
}

type indexFile struct {
	date string
	path string
}

// HistoryPath 返回 Capture 每日索引目录。
func HistoryPath(voiceRoot string) string { return filepath.Join(voiceRoot, historyDirName) }

// RecordingsPath 返回 Capture 会议录音产物目录。
func RecordingsPath(voiceRoot string) string { return filepath.Join(voiceRoot, recordingsDirName) }

// List 读取并查询 Capture 会议录音。索引目录尚未创建时，该来源按空集合处理。
func List(ctx context.Context, voiceRoot string, query Query) ([]Item, error) {
	files, err := listIndexFiles(ctx, voiceRoot, query)
	if err != nil {
		return nil, err
	}
	all := make([]sourceItem, 0)
	for _, index := range files {
		items, err := readIndexFile(ctx, index.path)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if query.From != nil && item.recordedTime.Before(*query.From) {
				continue
			}
			if query.To != nil && !item.recordedTime.Before(*query.To) {
				continue
			}
			if status := strings.TrimSpace(query.Status); status != "" && item.Status != status {
				continue
			}
			all = append(all, item)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].recordedTime.Equal(all[j].recordedTime) {
			return all[i].ID > all[j].ID
		}
		return all[i].recordedTime.After(all[j].recordedTime)
	})

	// 生产契约要求每个最终 ID 只写一次。若数据异常重复，保留最新一条并
	// 让调用方能看到诊断，而不是把重复行解释成状态更新流。
	seen := map[string]bool{}
	result := make([]Item, 0, len(all))
	for _, source := range all {
		if seen[source.ID] {
			for i := range result {
				if result[i].ID == source.ID && !contains(result[i].Diagnostics, "duplicate_index_entry") {
					result[i].Diagnostics = append(result[i].Diagnostics, "duplicate_index_entry")
					break
				}
			}
			continue
		}
		seen[source.ID] = true
		result = append(result, resolveItem(voiceRoot, source))
	}
	return result, nil
}

func listIndexFiles(ctx context.Context, voiceRoot string, query Query) ([]indexFile, error) {
	historyPath := HistoryPath(voiceRoot)
	entries, err := os.ReadDir(historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return []indexFile{}, nil
	}
	if err != nil {
		return nil, storageError(historyPath, "无法列出 YoooClaw Capture 会议录音索引", err)
	}

	var minDate, maxDate string
	if query.From != nil {
		minDate = query.From.In(time.Local).AddDate(0, 0, -1).Format("2006-01-02")
	}
	if query.To != nil {
		maxDate = query.To.In(time.Local).AddDate(0, 0, 1).Format("2006-01-02")
	}
	files := make([]indexFile, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		match := dailyIndexName.FindStringSubmatch(entry.Name())
		if entry.IsDir() || len(match) != 2 {
			continue
		}
		if _, err := time.Parse("2006-01-02", match[1]); err != nil {
			continue
		}
		if minDate != "" && match[1] < minDate || maxDate != "" && match[1] > maxDate {
			continue
		}
		files = append(files, indexFile{date: match[1], path: filepath.Join(historyPath, entry.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].date > files[j].date })
	return files, nil
}

func readIndexFile(ctx context.Context, path string) ([]sourceItem, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError(path, "无法打开 YoooClaw Capture 会议录音索引", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, storageError(path, "无法读取 YoooClaw Capture 会议录音索引信息", err)
	}
	if !info.Mode().IsRegular() {
		return nil, storageError(path, "YoooClaw Capture 会议录音索引不是普通文件", nil)
	}
	raw, err := io.ReadAll(io.LimitReader(file, info.Size()))
	if err != nil {
		return nil, storageError(path, "无法读取 YoooClaw Capture 会议录音索引", err)
	}
	if len(raw) == 0 {
		return []sourceItem{}, nil
	}
	lines := bytes.Split(raw, []byte{'\n'})
	if raw[len(raw)-1] != '\n' {
		lines = lines[:len(lines)-1]
	}
	items := make([]sourceItem, 0, len(lines))
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var item sourceItem
		if err := json.Unmarshal(line, &item); err != nil || !validSourceItem(&item) {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func validSourceItem(item *sourceItem) bool {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" || item.DurationMS < 0 {
		return false
	}
	recorded, err := time.Parse(time.RFC3339Nano, item.RecordedAt)
	if err != nil {
		return false
	}
	item.recordedTime = recorded
	return true
}

func resolveItem(voiceRoot string, source sourceItem) Item {
	item := Item{
		ID: source.ID, Title: source.Title,
		AudioRelPath: source.AudioRelPath, TranscriptRelPath: source.TranscriptRelPath,
		SummaryRelPath: source.SummaryRelPath, RecordedAt: source.RecordedAt,
		DurationMS: source.DurationMS, Status: source.Status,
		MissingArtifacts: []string{}, Diagnostics: []string{}, RecordedTime: source.recordedTime,
	}
	resolveArtifact := func(kind string, relative *string, path **string, available *bool) {
		if relative == nil || strings.TrimSpace(*relative) == "" {
			return
		}
		if parent := filepath.Base(filepath.Dir(filepath.Clean(*relative))); parent != source.ID {
			item.Diagnostics = append(item.Diagnostics, kind+"_parent_id_mismatch")
		}
		resolved, ok := fsutil.ResolveExistingRegularFile(voiceRoot, *relative)
		if !ok {
			item.MissingArtifacts = append(item.MissingArtifacts, kind)
			return
		}
		*available = true
		*path = &resolved
	}
	resolveArtifact("audio", source.AudioRelPath, &item.AudioPath, &item.HasAudio)
	resolveArtifact("transcript", source.TranscriptRelPath, &item.TranscriptPath, &item.HasTranscript)
	resolveArtifact("summary", source.SummaryRelPath, &item.SummaryPath, &item.HasSummary)
	return item
}

func storageError(path, message string, cause error) error {
	details := map[string]any{"path": path}
	if cause != nil {
		details["cause"] = cause.Error()
	}
	return errs.New(errs.CodeStorageUnavailable, message, details)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

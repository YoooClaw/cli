package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

var dailyHistoryName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)

// Repository 持有一个 profile 的 Voice JSONL 根目录。读取始终以文件快照进行，
// 不持有数据库连接，也不会回退读取旧的 voice.sqlite3。
type Repository struct {
	dir         string
	historyPath string
}

// Open 验证每日历史目录存在。缺少 audio-jsonl 时明确返回 storage not found，
// 即使同目录中仍有旧 SQLite 文件也不回退。
func Open(ctx context.Context, dir string) (*Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	historyPath := filepath.Join(dir, historyDirName)
	info, err := os.Stat(historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errs.New(
			errs.CodeVoiceStorageNotFound,
			"未找到语音输入 JSONL 历史目录",
			map[string]any{"path": historyPath},
		)
	}
	if err != nil || !info.IsDir() {
		return nil, storageError(historyPath, "语音输入 JSONL 历史目录不可用", err)
	}
	return &Repository{dir: dir, historyPath: historyPath}, nil
}

func storageError(path, message string, cause error) error {
	details := map[string]any{"path": path}
	if cause != nil {
		details["cause"] = cause.Error()
	}
	return errs.New(errs.CodeStorageUnavailable, message, details)
}

// Close 为兼容现有调用保留；JSONL Repository 不持有资源。
func (r *Repository) Close() error { return nil }

type sourceHistoryItem struct {
	VoiceID           string  `json:"voice_id"`
	StartedAt         string  `json:"started_at"`
	EndedAt           string  `json:"ended_at"`
	TimezoneOffsetMin int     `json:"timezone_offset_min"`
	DurationMS        int64   `json:"duration_ms"`
	Platform          string  `json:"platform"`
	AppID             string  `json:"app_id"`
	AppName           string  `json:"app_name"`
	WindowTitle       *string `json:"window_title"`
	Text              string  `json:"text"`
	Language          *string `json:"language"`
	CharCount         int64   `json:"char_count"`
	ResultStatus      string  `json:"result_status"`
	AudioRelPath      *string `json:"audio_rel_path"`

	startedTime time.Time
}

type historyFile struct {
	date string
	path string
}

func (r *Repository) historyFiles(ctx context.Context, opts Query) ([]historyFile, error) {
	entries, err := os.ReadDir(r.historyPath)
	if err != nil {
		return nil, storageError(r.historyPath, "无法列出语音输入 JSONL 历史", err)
	}

	// 文件日期使用采集设备当时的本地日。边界前后各保留一天，再逐条按
	// RFC3339 时间精确判断，避免调用方时区与采集时区不同造成漏查。
	var minDate, maxDate string
	if opts.From != nil {
		minDate = opts.From.In(time.Local).AddDate(0, 0, -1).Format("2006-01-02")
	}
	if opts.To != nil {
		maxDate = opts.To.In(time.Local).AddDate(0, 0, 1).Format("2006-01-02")
	}

	files := make([]historyFile, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		match := dailyHistoryName.FindStringSubmatch(entry.Name())
		if entry.IsDir() || len(match) != 2 {
			continue
		}
		date := match[1]
		if _, err := time.Parse("2006-01-02", date); err != nil {
			continue
		}
		if minDate != "" && date < minDate {
			continue
		}
		if maxDate != "" && date > maxDate {
			continue
		}
		files = append(files, historyFile{date: date, path: filepath.Join(r.historyPath, entry.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].date > files[j].date })
	return files, nil
}

func (r *Repository) readHistoryFile(ctx context.Context, path string) ([]sourceHistoryItem, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // writer 清理/切换文件与目录枚举并发发生
	}
	if err != nil {
		return nil, storageError(path, "无法打开语音输入 JSONL 历史", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, storageError(path, "无法读取语音输入 JSONL 文件信息", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(file, info.Size()))
	if err != nil {
		return nil, storageError(path, "无法读取语音输入 JSONL 历史", err)
	}
	if len(raw) == 0 {
		return []sourceHistoryItem{}, nil
	}

	// 活跃 writer 的最后一段只有在换行完整提交后才可见。
	lines := bytes.Split(raw, []byte{'\n'})
	if raw[len(raw)-1] != '\n' {
		lines = lines[:len(lines)-1]
	}
	items := make([]sourceHistoryItem, 0, len(lines))
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var item sourceHistoryItem
		if err := json.Unmarshal(line, &item); err != nil || !validSourceHistory(&item) {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].startedTime.Equal(items[j].startedTime) {
			return items[i].VoiceID > items[j].VoiceID
		}
		return items[i].startedTime.After(items[j].startedTime)
	})
	return items, nil
}

func validSourceHistory(item *sourceHistoryItem) bool {
	if item.DurationMS < 0 || item.CharCount < 0 {
		return false
	}
	started, err := time.Parse(time.RFC3339Nano, item.StartedAt)
	if err != nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, item.EndedAt); err != nil {
		return false
	}

	item.VoiceID = strings.TrimSpace(item.VoiceID)
	if item.VoiceID == "" {
		audioRelPath := ""
		if item.AudioRelPath != nil {
			audioRelPath = strings.TrimSpace(*item.AudioRelPath)
		}
		if audioRelPath == "" {
			return false
		}
		filename := filepath.Base(filepath.Clean(audioRelPath))
		stem := strings.TrimSpace(strings.TrimSuffix(filename, filepath.Ext(filename)))
		if stem == "" || stem == "." || stem == ".." {
			return false
		}
		item.VoiceID = stem
	}
	item.startedTime = started
	return true
}

func matchesQuery(item sourceHistoryItem, opts Query) bool {
	if opts.From != nil && item.startedTime.Before(*opts.From) {
		return false
	}
	if opts.To != nil && !item.startedTime.Before(*opts.To) {
		return false
	}
	if app := strings.TrimSpace(opts.App); app != "" &&
		!strings.EqualFold(strings.TrimSpace(item.AppName), app) &&
		!strings.EqualFold(strings.TrimSpace(item.AppID), app) {
		return false
	}
	if status := strings.TrimSpace(opts.Status); status != "" && !strings.EqualFold(strings.TrimSpace(item.ResultStatus), status) {
		return false
	}
	if language := strings.TrimSpace(opts.Language); language != "" {
		if item.Language == nil || !strings.EqualFold(strings.TrimSpace(*item.Language), language) {
			return false
		}
	}
	if keyword := strings.TrimSpace(opts.Keyword); keyword != "" &&
		!strings.Contains(strings.ToLower(item.Text), strings.ToLower(keyword)) {
		return false
	}
	return true
}

func (r *Repository) publicItem(item sourceHistoryItem) HistoryItem {
	result := HistoryItem{
		ID: item.VoiceID, StartedAt: item.StartedAt, EndedAt: item.EndedAt,
		TimezoneOffsetMin: item.TimezoneOffsetMin, DurationMS: item.DurationMS,
		Platform: item.Platform, AppName: item.AppName, WindowTitle: item.WindowTitle,
		Text: item.Text, Language: item.Language, CharCount: item.CharCount,
		ResultStatus: item.ResultStatus,
	}
	if item.AudioRelPath != nil {
		if path, ok := resolveAudioPath(r.dir, *item.AudioRelPath); ok {
			result.HasAudio = true
			result.AudioPath = &path
		}
	}
	return result
}

// List 返回按文件日期、文件内开始时间倒序排列的历史。Keyword 非空时只
// 搜索最终上屏文本。达到 Limit 后不再扫描更老的日期文件。
func (r *Repository) List(ctx context.Context, opts Query) ([]HistoryItem, error) {
	files, err := r.historyFiles(ctx, opts)
	if err != nil {
		return nil, err
	}
	items := make([]HistoryItem, 0)
	for _, historyFile := range files {
		sourceItems, err := r.readHistoryFile(ctx, historyFile.path)
		if err != nil {
			return nil, err
		}
		for _, sourceItem := range sourceItems {
			if !matchesQuery(sourceItem, opts) {
				continue
			}
			item := r.publicItem(sourceItem)
			if opts.HasAudio && !item.HasAudio {
				continue
			}
			items = append(items, item)
			if opts.Limit > 0 && len(items) >= opts.Limit {
				return items, nil
			}
		}
	}
	return items, nil
}

// Show 从新到旧查找 voice_id。数据库数字主键不参与查询。
func (r *Repository) Show(ctx context.Context, voiceID string) (*HistoryItem, error) {
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "voice_id 不能为空")
	}
	files, err := r.historyFiles(ctx, Query{})
	if err != nil {
		return nil, err
	}
	for _, historyFile := range files {
		items, err := r.readHistoryFile(ctx, historyFile.path)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.VoiceID == voiceID {
				result := r.publicItem(item)
				return &result, nil
			}
		}
	}
	return nil, errs.New(errs.CodeNotFound, fmt.Sprintf("语音输入历史不存在：%s", voiceID))
}

// Apps 返回查询范围内按真实 app_id 去重后的候选。app_id 为空时退化为按
// app_name 去重；同一个 app_id 使用最近一次非空 app_name 作为展示名称。
func (r *Repository) Apps(ctx context.Context, opts Query) ([]AppSummary, error) {
	files, err := r.historyFiles(ctx, opts)
	if err != nil {
		return nil, err
	}
	type aggregate struct {
		id     string
		name   string
		count  int64
		latest time.Time
		at     string
	}
	aggregates := map[string]*aggregate{}
	for _, historyFile := range files {
		items, err := r.readHistoryFile(ctx, historyFile.path)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if !matchesQuery(item, Query{From: opts.From, To: opts.To}) {
				continue
			}
			appID := strings.TrimSpace(item.AppID)
			appName := strings.TrimSpace(item.AppName)
			if appID == "" && appName == "" {
				continue
			}
			key := "id:" + strings.ToLower(appID)
			if appID == "" {
				key = "name:" + strings.ToLower(appName)
			}
			entry := aggregates[key]
			if entry == nil {
				entry = &aggregate{id: appID, name: appName}
				aggregates[key] = entry
			}
			entry.count++
			if entry.at == "" || item.startedTime.After(entry.latest) {
				entry.latest = item.startedTime
				entry.at = item.StartedAt
				entry.id = appID
				if appName != "" {
					entry.name = appName
				}
			} else if entry.name == "" && appName != "" {
				entry.name = appName
			}
		}
	}
	apps := make([]AppSummary, 0, len(aggregates))
	for _, entry := range aggregates {
		apps = append(apps, AppSummary{AppID: entry.id, AppName: entry.name, HistoryCount: entry.count, LatestAt: entry.at})
	}
	sort.Slice(apps, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, apps[i].LatestAt)
		right, _ := time.Parse(time.RFC3339Nano, apps[j].LatestAt)
		if left.Equal(right) {
			leftName := strings.ToLower(apps[i].AppName)
			rightName := strings.ToLower(apps[j].AppName)
			if leftName == rightName {
				return strings.ToLower(apps[i].AppID) < strings.ToLower(apps[j].AppID)
			}
			return leftName < rightName
		}
		return left.After(right)
	})
	return apps, nil
}

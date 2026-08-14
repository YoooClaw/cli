// Package recording 读取/写入录音索引（recordings/index.json）与状态事件流
// （recordings/state/events.jsonl）。
package recording

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	audioDirName          = "audio"
	transcriptDataDirName = "transcript-data"
	transcriptsDirName    = "transcripts"
	summariesDirName      = "summaries"
	indexFileName         = "index.json"
)

// Logger 是存储/同步层依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
	Error(string)
}

// Marker 是录音关键点标记。
type Marker struct {
	Index       int     `json:"index"`
	TimestampMS float64 `json:"timestamp_ms"`
}

// Metadata 是录音元数据（index.json entry.metadata）。
type Metadata struct {
	Name            string   `json:"name"`
	DurationSec     float64  `json:"duration_sec"`
	DurationDisplay string   `json:"duration_display"`
	FileSizeDisplay string   `json:"file_size_display,omitempty"`
	CreatedAt       string   `json:"created_at"`
	Status          string   `json:"transfer_status,omitempty"`
	Location        any      `json:"location,omitempty"`
	OssAudioURL     string   `json:"oss_audio_url"`
	OssSrtURL       string   `json:"oss_srt_url,omitempty"`
	Markers         []Marker `json:"markers,omitempty"`
}

// Entry 是一条录音索引项。
type Entry struct {
	ID                 string   `json:"id"`
	ClientLabel        string   `json:"clientLabel,omitempty"`
	Metadata           Metadata `json:"metadata"`
	Status             string   `json:"status"`
	AudioStatus        string   `json:"audioStatus,omitempty"`
	AudioSourceURL     string   `json:"audioSourceUrl,omitempty"`
	AudioFile          string   `json:"audioFile,omitempty"`
	SrtFile            string   `json:"srtFile,omitempty"`
	TranscriptDataFile string   `json:"transcriptDataFile,omitempty"`
	TranscriptFile     string   `json:"transcriptFile,omitempty"`
	SummaryFile        string   `json:"summaryFile,omitempty"`
	Title              string   `json:"title,omitempty"`
	LastError          string   `json:"lastError,omitempty"`
	IngestedAt         string   `json:"ingestedAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

type indexWrapper struct {
	Recordings []Entry `json:"recordings"`
}

// Storage 是 recordings/ 目录的写侧存储。
type Storage struct {
	dir               string
	audioDir          string
	transcriptDataDir string
	transcriptsDir    string
	summariesDir      string
	indexPath         string
	logger            Logger
	mu                sync.Mutex
	index             indexWrapper
	audioLocks        map[string]*sync.Mutex
}

// NewStorage 构造录音存储；dir 为 recordings 目录。
func NewStorage(dir string, logger Logger) *Storage {
	return &Storage{
		dir:               dir,
		audioDir:          filepath.Join(dir, audioDirName),
		transcriptDataDir: filepath.Join(dir, transcriptDataDirName),
		transcriptsDir:    filepath.Join(dir, transcriptsDirName),
		summariesDir:      filepath.Join(dir, summariesDirName),
		indexPath:         filepath.Join(dir, indexFileName),
		logger:            logger,
		index:             indexWrapper{Recordings: []Entry{}},
		audioLocks:        map[string]*sync.Mutex{},
	}
}

// Init 准备目录并加载索引。
func (s *Storage) Init() error {
	for _, dir := range []string{s.audioDir, s.transcriptDataDir, s.transcriptsDir, s.summariesDir} {
		if err := fsutil.EnsureDir(dir, fsutil.DirMode); err != nil {
			return err
		}
	}
	s.loadIndex()
	s.logger.Info("录音存储已初始化: " + s.dir)
	return nil
}

// Close 当前无需额外清理。
func (s *Storage) Close() error { return nil }

func (s *Storage) loadIndex() {
	var wrapper indexWrapper
	exists, err := fsutil.ReadJSON(s.indexPath, &wrapper)
	if err != nil || !exists || wrapper.Recordings == nil {
		s.index = indexWrapper{Recordings: []Entry{}}
		return
	}
	needsRewrite := false
	now := nowISO()
	for i := range wrapper.Recordings {
		entry := &wrapper.Recordings[i]
		durationSec := normalizeDurationSeconds(entry.Metadata.DurationSec)
		durationDisplay := FormatDurationDisplay(durationSec)
		if entry.Metadata.DurationSec != durationSec || entry.Metadata.DurationDisplay != durationDisplay {
			entry.Metadata.DurationSec = durationSec
			entry.Metadata.DurationDisplay = durationDisplay
			needsRewrite = true
		}
		switch wrapper.Recordings[i].Status {
		case "transcribing":
			wrapper.Recordings[i].Status = "transcribe_failed"
			if strings.TrimSpace(wrapper.Recordings[i].LastError) == "" {
				wrapper.Recordings[i].LastError = "转写任务已中断，请重新发起转写"
			}
			wrapper.Recordings[i].UpdatedAt = now
			needsRewrite = true
		case "syncing_openclaw", "sync_failed":
			// 旧版 recordings.sync 遗留的中间态，sync 已移除，归一到 synced。
			wrapper.Recordings[i].Status = StatusSynced
			wrapper.Recordings[i].UpdatedAt = now
			needsRewrite = true
		}
		if entry.AudioFile != "" {
			audioPath := filepath.Join(s.dir, entry.AudioFile)
			if info, err := os.Stat(audioPath); err == nil && !info.IsDir() {
				display := FormatShortFileSize(info.Size())
				if entry.Metadata.FileSizeDisplay != display {
					entry.Metadata.FileSizeDisplay = display
					needsRewrite = true
				}
				if entry.AudioSourceURL == "" {
					entry.AudioSourceURL = strings.TrimSpace(entry.Metadata.OssAudioURL)
					needsRewrite = true
				}
				if entry.AudioStatus == "" {
					entry.AudioStatus = AudioStatusDownloaded
					needsRewrite = true
				}
			} else {
				// 索引不能继续声称一个不存在的文件可用。保留 OSS URL，启动后的
				// recovery 会重新下载。
				entry.AudioFile = ""
				entry.AudioSourceURL = ""
				entry.AudioStatus = AudioStatusPending
				entry.Metadata.FileSizeDisplay = ""
				if strings.TrimSpace(entry.LastError) == "" {
					entry.LastError = "本地音频文件缺失，等待重新下载"
				}
				entry.UpdatedAt = now
				needsRewrite = true
			}
		} else if strings.TrimSpace(entry.Metadata.OssAudioURL) != "" && entry.AudioStatus == "" {
			// 兼容旧索引：历史版本会把下载错误清掉，只留下空 audioFile。
			entry.AudioStatus = AudioStatusPending
			needsRewrite = true
		}
	}
	s.index = wrapper
	if needsRewrite {
		_ = s.saveIndexLocked()
	}
}

func (s *Storage) saveIndexLocked() error {
	return fsutil.WriteJSON(s.indexPath, s.index, fsutil.ConfigFileMode)
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// AudioDir 返回 audio/ 绝对路径。
func (s *Storage) AudioDir() string { return s.audioDir }

// TranscriptDataDir 返回 transcript-data/ 绝对路径。
func (s *Storage) TranscriptDataDir() string { return s.transcriptDataDir }

// TranscriptsDir 返回 transcripts/ 绝对路径。
func (s *Storage) TranscriptsDir() string { return s.transcriptsDir }

// SummariesDir 返回 summaries/ 绝对路径。
func (s *Storage) SummariesDir() string { return s.summariesDir }

// Ingest 写入/更新录音元数据（result.write 新建录音时调用）。
func (s *Storage) Ingest(recordingID string, metadata Metadata, clientLabel string) (Entry, error) {
	if clientLabel == "" {
		clientLabel = "default"
	}
	metadata.DurationSec = normalizeDurationSeconds(metadata.DurationSec)
	metadata.DurationDisplay = FormatDurationDisplay(metadata.DurationSec)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowISO()
	if idx := s.findIndexLocked(recordingID); idx >= 0 {
		entry := &s.index.Recordings[idx]
		canPreserveState := entry.Metadata.OssAudioURL == metadata.OssAudioURL && entry.AudioFile != ""
		if canPreserveState {
			entry.Metadata = metadata
			entry.ClientLabel = clientLabel
			entry.UpdatedAt = now
			if err := s.saveIndexLocked(); err != nil {
				return Entry{}, err
			}
			s.logger.Info("录音元数据已更新: " + recordingID + "（同音频，保留状态 " + entry.Status + "）")
			return *entry, nil
		}

		s.removeRelativeLocked(entry.TranscriptDataFile)
		s.removeRelativeLocked(entry.TranscriptFile)
		s.removeRelativeLocked(entry.SummaryFile)
		entry.Metadata = metadata
		entry.ClientLabel = clientLabel
		entry.Status = StatusSynced
		if strings.TrimSpace(metadata.OssAudioURL) != "" {
			entry.AudioStatus = AudioStatusPending
		}
		entry.TranscriptDataFile = ""
		entry.TranscriptFile = ""
		entry.SummaryFile = ""
		entry.Title = ""
		entry.LastError = ""
		entry.UpdatedAt = now
		if err := s.saveIndexLocked(); err != nil {
			return Entry{}, err
		}
		s.logger.Info("录音元数据已更新: " + recordingID)
		return *entry, nil
	}

	entry := Entry{
		ID:          recordingID,
		ClientLabel: clientLabel,
		Metadata:    metadata,
		Status:      StatusSynced,
		AudioStatus: func() string {
			if strings.TrimSpace(metadata.OssAudioURL) != "" {
				return AudioStatusPending
			}
			return ""
		}(),
		IngestedAt: now,
		UpdatedAt:  now,
	}
	s.index.Recordings = append(s.index.Recordings, entry)
	if err := s.saveIndexLocked(); err != nil {
		return Entry{}, err
	}
	s.logger.Info("录音元数据已入库: " + recordingID)
	return entry, nil
}

func (s *Storage) removeRelativeLocked(rel string) {
	if rel == "" {
		return
	}
	_ = os.Remove(filepath.Join(s.dir, rel))
}

// FindByID 查询单条录音，返回副本。
func (s *Storage) FindByID(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(id)
	if idx < 0 {
		return Entry{}, false
	}
	return s.index.Recordings[idx], true
}

func (s *Storage) findIndexLocked(id string) int {
	for i, r := range s.index.Recordings {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// ListAll 返回按 created_at 倒序排列的录音副本。
func (s *Storage) ListAll() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Entry(nil), s.index.Recordings...)
	SortByCreatedDesc(out)
	return out
}

// ListByStatus 按状态过滤。
func (s *Storage) ListByStatus(status string) []Entry {
	all := s.ListAll()
	out := all[:0]
	for _, entry := range all {
		if entry.Status == status {
			out = append(out, entry)
		}
	}
	return out
}

// Rename 更新录音名称。
func (s *Storage) Rename(recordingID, name string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return Entry{}, false, nil
	}
	s.index.Recordings[idx].Metadata.Name = name
	s.index.Recordings[idx].UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return Entry{}, false, err
	}
	return s.index.Recordings[idx], true, nil
}

// Delete 删除录音。localOnly 为 true 时保留索引，仅清掉本地文件引用。
func (s *Storage) Delete(recordingID string, localOnly bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return false, nil
	}
	entry := &s.index.Recordings[idx]
	for _, rel := range []string{entry.AudioFile, entry.SrtFile, entry.TranscriptDataFile, entry.TranscriptFile, entry.SummaryFile} {
		s.removeRelativeLocked(rel)
	}
	if localOnly {
		entry.AudioFile = ""
		entry.AudioSourceURL = ""
		entry.Metadata.FileSizeDisplay = ""
		if strings.TrimSpace(entry.Metadata.OssAudioURL) != "" {
			entry.AudioStatus = AudioStatusPending
		} else {
			entry.AudioStatus = ""
		}
		entry.SrtFile = ""
		entry.TranscriptDataFile = ""
		entry.TranscriptFile = ""
		entry.SummaryFile = ""
		entry.UpdatedAt = nowISO()
	} else {
		s.index.Recordings = append(s.index.Recordings[:idx], s.index.Recordings[idx+1:]...)
	}
	if err := s.saveIndexLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateStatus 按状态机校验并更新状态。
func (s *Storage) UpdateStatus(recordingID, newStatus string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return Entry{}, os.ErrNotExist
	}
	entry := &s.index.Recordings[idx]
	if err := ValidateTransition(entry.Status, newStatus); err != nil {
		return Entry{}, err
	}
	entry.Status = newStatus
	entry.UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return Entry{}, err
	}
	s.logger.Info("录音状态更新: " + recordingID + " -> " + newStatus)
	return *entry, nil
}

// MarkResultWritten 在外部结果（result.write）写入后直接置为 transcribed。
// 与插件 storage.markResultWritten 一致：绕过状态机校验，因为 synced/syncing →
// transcribed 不是状态机的合法边，但这是带外（out-of-band）写入的预期终态。
func (s *Storage) MarkResultWritten(recordingID string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return Entry{}, os.ErrNotExist
	}
	entry := &s.index.Recordings[idx]
	entry.Status = StatusTranscribed
	if entry.AudioStatus != AudioStatusFailed {
		entry.LastError = ""
	}
	entry.UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return Entry{}, err
	}
	s.logger.Info("录音结果已写入: " + recordingID)
	return *entry, nil
}

// SetAudioFile 记录本地音频文件。
func (s *Storage) SetAudioFile(recordingID, filename string) error {
	display := ""
	if info, err := os.Stat(filepath.Join(s.audioDir, filename)); err == nil && !info.IsDir() {
		display = FormatShortFileSize(info.Size())
	}
	return s.updateEntry(recordingID, func(entry *Entry) {
		next := audioDirName + "/" + filename
		if entry.AudioFile != "" && entry.AudioFile != next {
			s.removeRelativeLocked(entry.AudioFile)
		}
		entry.AudioFile = next
		entry.AudioSourceURL = strings.TrimSpace(entry.Metadata.OssAudioURL)
		entry.AudioStatus = AudioStatusDownloaded
		entry.Metadata.FileSizeDisplay = display
		entry.LastError = ""
	})
}

// SetResultAudioPending 持久化 result.write 携带的最新 OSS URL，并把本地音频
// 标记为待下载。即使 daemon 在后台下载前退出，下一次启动也能从索引恢复任务。
func (s *Storage) SetResultAudioPending(recordingID, ossURL string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		nextURL := strings.TrimSpace(ossURL)
		entry.Metadata.OssAudioURL = nextURL
		if strings.TrimSpace(entry.AudioSourceURL) == nextURL && entry.AudioFile != "" {
			if info, err := os.Stat(filepath.Join(s.dir, entry.AudioFile)); err == nil && !info.IsDir() {
				entry.AudioStatus = AudioStatusDownloaded
				entry.Metadata.FileSizeDisplay = FormatShortFileSize(info.Size())
				entry.LastError = ""
				return
			}
		}
		entry.AudioStatus = AudioStatusPending
		if strings.HasPrefix(entry.LastError, "音频下载失败:") ||
			entry.LastError == "本地音频文件缺失，等待重新下载" {
			entry.LastError = ""
		}
	})
}

// SetResultAudioDownloading 标记当前 OSS URL 正在下载。URL 已被更新时忽略旧任务，
// 防止迟到的 goroutine 覆盖新结果的状态。
func (s *Storage) SetResultAudioDownloading(recordingID, ossURL string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return false, os.ErrNotExist
	}
	entry := &s.index.Recordings[idx]
	if strings.TrimSpace(entry.Metadata.OssAudioURL) != strings.TrimSpace(ossURL) {
		return false, nil
	}
	if entry.AudioStatus == AudioStatusDownloaded && entry.AudioFile != "" {
		if info, statErr := os.Stat(filepath.Join(s.dir, entry.AudioFile)); statErr == nil && !info.IsDir() {
			return false, nil
		}
	}
	entry.AudioStatus = AudioStatusDownloading
	entry.UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// SetResultAudioFailed 只更新仍指向同一个 OSS URL 的任务，避免旧请求覆盖新状态。
func (s *Storage) SetResultAudioFailed(recordingID, ossURL, message string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return false, os.ErrNotExist
	}
	entry := &s.index.Recordings[idx]
	if strings.TrimSpace(entry.Metadata.OssAudioURL) != strings.TrimSpace(ossURL) {
		return false, nil
	}
	entry.AudioStatus = AudioStatusFailed
	entry.LastError = strings.TrimSpace(message)
	entry.UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// CommitResultAudioDownloaded 在确认 OSS URL 仍是最新版本后，把同目录的暂存文件
// 原子替换到正式路径并更新索引。旧任务即使较晚完成，也不能覆盖新音频。
func (s *Storage) CommitResultAudioDownloaded(recordingID, ossURL, stagedPath, filename string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return false, os.ErrNotExist
	}
	entry := &s.index.Recordings[idx]
	if strings.TrimSpace(entry.Metadata.OssAudioURL) != strings.TrimSpace(ossURL) {
		return false, nil
	}
	if filepath.Base(filename) != filename {
		return false, os.ErrInvalid
	}
	info, err := os.Stat(stagedPath)
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, os.ErrInvalid
	}
	next := audioDirName + "/" + filename
	destPath := filepath.Join(s.audioDir, filename)
	if err := os.Rename(stagedPath, destPath); err != nil {
		return false, err
	}
	_ = os.Chmod(destPath, 0o600)
	previous := entry.AudioFile
	entry.AudioFile = next
	entry.AudioSourceURL = strings.TrimSpace(ossURL)
	entry.AudioStatus = AudioStatusDownloaded
	entry.Metadata.FileSizeDisplay = FormatShortFileSize(info.Size())
	entry.LastError = ""
	entry.UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return false, err
	}
	if previous != "" && previous != next {
		s.removeRelativeLocked(previous)
	}
	return true, nil
}

// ListMissingAudio 返回索引中有 OSS URL、但本地文件为空或已经丢失的录音。
func (s *Storage) ListMissingAudio() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0)
	for _, entry := range s.index.Recordings {
		if strings.TrimSpace(entry.Metadata.OssAudioURL) == "" {
			continue
		}
		missing := entry.AudioFile == ""
		if !missing {
			info, err := os.Stat(filepath.Join(s.dir, entry.AudioFile))
			missing = err != nil || info.IsDir()
		}
		sourceOutdated := strings.TrimSpace(entry.AudioSourceURL) != strings.TrimSpace(entry.Metadata.OssAudioURL)
		if missing || sourceOutdated || entry.AudioStatus == AudioStatusFailed {
			out = append(out, entry)
		}
	}
	return out
}

// lockAudioDownload 把同一 recordingId 的下载串行化，避免重复 result.write 并发
// 写同一个目标文件。不同录音仍可并行。
func (s *Storage) lockAudioDownload(recordingID string) func() {
	s.mu.Lock()
	lock := s.audioLocks[recordingID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.audioLocks[recordingID] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// SetTranscriptDataFile 记录转写 JSON 文件。
func (s *Storage) SetTranscriptDataFile(recordingID, filename string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		next := transcriptDataDirName + "/" + filename
		if entry.TranscriptDataFile != "" && entry.TranscriptDataFile != next {
			s.removeRelativeLocked(entry.TranscriptDataFile)
		}
		entry.TranscriptDataFile = next
	})
}

// SetTranscriptFile 记录转写 Markdown 文件。
func (s *Storage) SetTranscriptFile(recordingID, filename string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		next := transcriptsDirName + "/" + filename
		if entry.TranscriptFile != "" && entry.TranscriptFile != next {
			s.removeRelativeLocked(entry.TranscriptFile)
		}
		entry.TranscriptFile = next
	})
}

// SetSummaryFile 记录摘要文件。
func (s *Storage) SetSummaryFile(recordingID, filename string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		next := summariesDirName + "/" + filename
		if entry.SummaryFile != "" && entry.SummaryFile != next {
			s.removeRelativeLocked(entry.SummaryFile)
		}
		entry.SummaryFile = next
	})
}

// SetTitle 记录转写标题。
func (s *Storage) SetTitle(recordingID, title string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		entry.Title = strings.TrimSpace(title)
	})
}

// SetLastError 记录或清除最近错误。
func (s *Storage) SetLastError(recordingID, message string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		entry.LastError = strings.TrimSpace(message)
	})
}

func (s *Storage) updateEntry(recordingID string, mutate func(*Entry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return os.ErrNotExist
	}
	mutate(&s.index.Recordings[idx])
	s.index.Recordings[idx].UpdatedAt = nowISO()
	return s.saveIndexLocked()
}

// AudioFilename 生成音频文件名。
func AudioFilename(recordingID, ossURL string) string {
	return recordingID + extractAudioExt(ossURL)
}

// TranscriptDataFilename 生成转写 JSON 文件名。
func TranscriptDataFilename(recordingID string) string { return recordingID + ".json" }

// TranscriptFilename 生成转写 Markdown 文件名。
func TranscriptFilename(recordingID, title, createdAt string) string {
	return artifactFilename(recordingID, title, createdAt)
}

// SummaryFilename 生成摘要文件名。
func SummaryFilename(recordingID, title, createdAt string) string {
	return artifactFilename(recordingID, title, createdAt)
}

// artifactFilename 按 <YYYYMMDDHH>_<标题>_<ID>.md 组装文件名：时间和标题在前，
// 按文件名排序即按时间排序，搜索时打头的也是有区分度的字段；ID 收尾，仍能把文件
// 对回 index.json 里的条目。时间戳取宿主本地时区，小时粒度用于区分同一天的多场会议。
//
// 注意这只是写入时的命名，读取一律以 entry.TranscriptFile / entry.SummaryFile 为准，
// 没有任何地方反解析文件名。时间无法解析或标题为空时各自省略该段，最差退回 <ID>.md。
func artifactFilename(recordingID, title, createdAt string) string {
	parts := make([]string, 0, 3)
	if parsed, ok := parseRecordingTime(createdAt); ok {
		parts = append(parts, parsed.In(time.Local).Format("2006010215"))
	}
	if safe := sanitizeFilename(title); safe != "" {
		parts = append(parts, safe)
	}
	parts = append(parts, recordingID)
	return strings.Join(parts, "_") + ".md"
}

// AudioFilePath 返回音频文件绝对路径。
func (s *Storage) AudioFilePath(recordingID, ossURL string) string {
	return filepath.Join(s.audioDir, AudioFilename(recordingID, ossURL))
}

func extractAudioExt(rawURL string) string {
	if rawURL != "" {
		if u, err := url.Parse(rawURL); err == nil {
			if ext := filepath.Ext(u.Path); validExt(ext) {
				return strings.ToLower(ext)
			}
		}
		if ext := filepath.Ext(strings.Split(rawURL, "?")[0]); validExt(ext) {
			return strings.ToLower(ext)
		}
	}
	return ".ogg"
}

var extRE = regexp.MustCompile(`^\.[A-Za-z0-9]+$`)

func validExt(ext string) bool {
	if ext == "" || len(ext) > 12 {
		return false
	}
	return extRE.MatchString(ext)
}

var badFilenameChars = strings.NewReplacer("/", "", "\\", "", ":", "", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")

// sanitizeFilename 剥掉文件名非法字符和控制字符，并按码点（而非字节）截断，
// 避免把多字节字符切成半个。60 码点即使全是 CJK 也约 180 字节，加上时间戳、ID
// 和分隔符仍在 255 字节的文件名上限内。
func sanitizeFilename(s string) string {
	out := strings.TrimSpace(badFilenameChars.Replace(s))
	runes := make([]rune, 0, len(out))
	for _, r := range out {
		if unicode.IsControl(r) {
			continue
		}
		runes = append(runes, r)
		if len(runes) == 60 {
			break
		}
	}
	return strings.TrimSpace(string(runes))
}

// ReadIndex 读取 recordings/index.json 的 recordings[]；目录/文件不存在返回空。
func ReadIndex(recordingsDir string) []Entry {
	raw, err := os.ReadFile(filepath.Join(recordingsDir, indexFileName))
	if err != nil {
		return nil
	}
	var wrapper indexWrapper
	if json.Unmarshal(raw, &wrapper) != nil {
		return nil
	}
	return wrapper.Recordings
}

var recordingTimeLayoutsWithZone = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999 Z07:00",
	"2006-01-02 15:04:05.999999999 Z0700",
}

var recordingTimeLayoutsLocal = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

// parseRecordingTime 兼容标准 RFC3339 和历史客户端上报的无时区/空格格式。
// 无时区时间按运行 CLI 的本地时区解释，与客户端界面展示的本地录音时间一致。
func parseRecordingTime(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range recordingTimeLayoutsWithZone {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	for _, layout := range recordingTimeLayoutsLocal {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// ParseTime 是录音时间解析的导出入口，供 CLI 的 --from/--to 自然日筛选复用。
func ParseTime(raw string) (time.Time, bool) {
	return parseRecordingTime(raw)
}

// recordingSortTime 优先使用录音创建时间；历史脏数据无法解析时，依次回退到
// 入库时间和更新时间，避免单条无效 created_at 固定占据 latest。
func recordingSortTime(entry Entry) (time.Time, bool) {
	for _, value := range []string{entry.Metadata.CreatedAt, entry.IngestedAt, entry.UpdatedAt} {
		if parsed, ok := parseRecordingTime(value); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// EffectiveTime 返回录音用于查询和排序的有效时间。正常数据取真实 created_at；
// 历史脏数据才依次回退到 ingestedAt、updatedAt。
func EffectiveTime(entry Entry) (time.Time, bool) {
	return recordingSortTime(entry)
}

// SortByCreatedDesc 按真实时间点倒序排序，而不是按 created_at 原始字符串排序。
func SortByCreatedDesc(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, leftOK := recordingSortTime(entries[i])
		right, rightOK := recordingSortTime(entries[j])
		if leftOK != rightOK {
			return leftOK
		}
		if !leftOK || left.Equal(right) {
			return false
		}
		return left.After(right)
	})
}

// Event 是录音状态事件（events.jsonl 一行），保留全部原始字段。
type Event = map[string]any

// ReadEvents 读取 events.jsonl；损坏行跳过。
func ReadEvents(eventsPath string) []Event {
	raw, err := os.ReadFile(eventsPath)
	if err != nil {
		return nil
	}
	var out []Event
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(t), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

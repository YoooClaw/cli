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
	Name          string   `json:"name"`
	DurationSec   float64  `json:"duration_sec"`
	FileSizeBytes int64    `json:"file_size_bytes"`
	CreatedAt     string   `json:"created_at"`
	Status        string   `json:"transfer_status,omitempty"`
	Location      any      `json:"location,omitempty"`
	OssAudioURL   string   `json:"oss_audio_url"`
	OssSrtURL     string   `json:"oss_srt_url,omitempty"`
	Markers       []Marker `json:"markers,omitempty"`
}

// Entry 是一条录音索引项。
type Entry struct {
	ID                 string   `json:"id"`
	ClientLabel        string   `json:"clientLabel,omitempty"`
	Metadata           Metadata `json:"metadata"`
	Status             string   `json:"status"`
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
		if wrapper.Recordings[i].Status == "transcribing" {
			wrapper.Recordings[i].Status = "transcribe_failed"
			if strings.TrimSpace(wrapper.Recordings[i].LastError) == "" {
				wrapper.Recordings[i].LastError = "转写任务已中断，请重新发起转写"
			}
			wrapper.Recordings[i].UpdatedAt = now
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

// Ingest 收到 recordings.sync 后写入/更新元数据。
func (s *Storage) Ingest(recordingID string, metadata Metadata, clientLabel string) (Entry, error) {
	if clientLabel == "" {
		clientLabel = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowISO()
	if idx := s.findIndexLocked(recordingID); idx >= 0 {
		entry := &s.index.Recordings[idx]
		sameAudioURL := entry.Metadata.OssAudioURL == metadata.OssAudioURL
		canPreserveSyncState := sameAudioURL &&
			entry.AudioFile != "" &&
			entry.Status != "syncing_openclaw" &&
			entry.Status != "sync_failed"
		if canPreserveSyncState {
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
		entry.Status = "syncing_openclaw"
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
		Status:      "syncing_openclaw",
		IngestedAt:  now,
		UpdatedAt:   now,
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
	entry.LastError = ""
	entry.UpdatedAt = nowISO()
	if err := s.saveIndexLocked(); err != nil {
		return Entry{}, err
	}
	s.logger.Info("录音结果已写入: " + recordingID)
	return *entry, nil
}

// SetAudioFile 记录本地音频文件。
func (s *Storage) SetAudioFile(recordingID, filename string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		next := audioDirName + "/" + filename
		if entry.AudioFile != "" && entry.AudioFile != next {
			s.removeRelativeLocked(entry.AudioFile)
		}
		entry.AudioFile = next
	})
}

// SetSrtFile 记录本地打点文件。
func (s *Storage) SetSrtFile(recordingID, filename string) error {
	return s.updateEntry(recordingID, func(entry *Entry) {
		entry.SrtFile = audioDirName + "/" + filename
	})
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

// SrtFilename 生成打点文件名。
func SrtFilename(recordingID string) string { return recordingID + ".srt" }

// TranscriptDataFilename 生成转写 JSON 文件名。
func TranscriptDataFilename(recordingID string) string { return recordingID + ".json" }

// TranscriptFilename 生成转写 Markdown 文件名。
func TranscriptFilename(recordingID, title string) string {
	safe := sanitizeFilename(title)
	if safe != "" {
		return recordingID + "_" + safe + ".md"
	}
	return recordingID + ".md"
}

// SummaryFilename 生成摘要文件名。
func SummaryFilename(recordingID string) string { return recordingID + ".md" }

// AudioFilePath 返回音频文件绝对路径。
func (s *Storage) AudioFilePath(recordingID, ossURL string) string {
	return filepath.Join(s.audioDir, AudioFilename(recordingID, ossURL))
}

// SrtFilePath 返回打点文件绝对路径。
func (s *Storage) SrtFilePath(recordingID string) string {
	return filepath.Join(s.audioDir, SrtFilename(recordingID))
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

func validExt(ext string) bool {
	if ext == "" || len(ext) > 12 {
		return false
	}
	return regexp.MustCompile(`^\.[A-Za-z0-9]+$`).MatchString(ext)
}

var badFilenameChars = strings.NewReplacer("/", "", "\\", "", ":", "", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")

func sanitizeFilename(s string) string {
	out := strings.TrimSpace(badFilenameChars.Replace(s))
	runes := []rune(out)
	if len(runes) > 20 {
		runes = runes[:20]
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

// SortByCreatedDesc 按 created_at 倒序排序。
func SortByCreatedDesc(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Metadata.CreatedAt > entries[j].Metadata.CreatedAt
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

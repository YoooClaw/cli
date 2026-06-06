// Package recording 读取录音索引（recordings/index.json）与状态事件流
// （recordings/state/events.jsonl），对齐 TS 版 cli helpers + commands/recording.ts。
package recording

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Metadata 是录音元数据（index.json entry.metadata）。
type Metadata struct {
	Name          string  `json:"name"`
	DurationSec   float64 `json:"duration_sec"`
	FileSizeBytes int64   `json:"file_size_bytes"`
	CreatedAt     string  `json:"created_at"`
	Status        string  `json:"transfer_status"`
	Location      any     `json:"location,omitempty"`
	Markers       any     `json:"markers,omitempty"`
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

// ReadIndex 读取 recordings/index.json 的 recordings[]；目录/文件不存在返回空。
func ReadIndex(recordingsDir string) []Entry {
	raw, err := os.ReadFile(filepath.Join(recordingsDir, "index.json"))
	if err != nil {
		return nil
	}
	var wrapper struct {
		Recordings []Entry `json:"recordings"`
	}
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

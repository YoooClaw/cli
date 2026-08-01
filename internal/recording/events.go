package recording

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// StatusEvent 是录音状态变化事件。
type StatusEvent struct {
	RecordingID        string           `json:"recordingId"`
	TransferStatus     string           `json:"transfer_status"`
	AudioStatus        string           `json:"audio_status,omitempty"`
	AudioFile          string           `json:"audioFile,omitempty"`
	SrtFile            string           `json:"srtFile,omitempty"`
	TranscriptDataFile string           `json:"transcriptDataFile,omitempty"`
	TranscriptFile     string           `json:"transcriptFile,omitempty"`
	SummaryFile        string           `json:"summaryFile,omitempty"`
	Transcript         []TranscriptItem `json:"transcript,omitempty"`
	Summary            string           `json:"summary,omitempty"`
	Title              string           `json:"title"`
	UpdatedAt          string           `json:"updatedAt"`
	Error              string           `json:"error,omitempty"`
}

// EventLog 是 recordings/state/events.jsonl append-only 日志。
type EventLog struct {
	Path string
}

// NewEventLog 构造事件日志。
func NewEventLog(recordingsDir string) *EventLog {
	path := filepath.Join(recordingsDir, "state", "events.jsonl")
	_ = fsutil.EnsureDir(filepath.Dir(path), fsutil.DirMode)
	return &EventLog{Path: path}
}

// Append 追加一条状态事件。
func (l *EventLog) Append(event StatusEvent) error {
	record := map[string]any{"ts": nowISO()}
	data, _ := json.Marshal(event)
	_ = json.Unmarshal(data, &record)
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

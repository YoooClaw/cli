package capturerecording

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

func captureSource(id string, recorded time.Time) map[string]any {
	base := filepath.ToSlash(filepath.Join("recordings", recorded.Format("2006/01"), id))
	return map[string]any{
		"id": id, "title": "会议 " + id,
		"audio_rel_path":      base + "/audio.m4a",
		"transcript_rel_path": base + "/transcript.json",
		"summary_rel_path":    base + "/summary.md",
		"recorded_at":         recorded.Format(time.RFC3339), "duration_ms": 134340,
		"status": "completed",
	}
}

func writeCaptureDay(t *testing.T, root, date string, rows []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(HistoryPath(root), 0o700); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 0)
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(filepath.Join(HistoryPath(root), date+".jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestListResolvesArtifactsAndReportsMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "voice")
	recorded := time.Date(2026, 8, 20, 14, 21, 5, 0, time.FixedZone("UTC+8", 8*60*60))
	id := "531e3f9f-37c3-4c86-8896-914b263986b2"
	row := captureSource(id, recorded)
	writeCaptureDay(t, root, "2026-08-20", []map[string]any{row})
	artifactDir := filepath.Join(RecordingsPath(root), "2026", "08", id)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "audio.m4a"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "summary.md"), []byte("summary"), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := List(context.Background(), root, Query{})
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %+v, err=%v", items, err)
	}
	item := items[0]
	if !item.HasAudio || !item.HasSummary || item.HasTranscript {
		t.Fatalf("artifact facts = %+v", item)
	}
	if item.AudioPath == nil || !filepath.IsAbs(*item.AudioPath) || item.TranscriptPath != nil {
		t.Fatalf("resolved absolute paths = %+v", item)
	}
	if len(item.MissingArtifacts) != 1 || item.MissingArtifacts[0] != "transcript" {
		t.Fatalf("missing_artifacts = %+v", item.MissingArtifacts)
	}
	if item.Status != "completed" {
		t.Fatalf("status must be passed through: %+v", item)
	}
}

func TestListSkipsBadRowsDeduplicatesAndRejectsUnsafePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "voice")
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	older := captureSource("same", base)
	newer := captureSource("same", base.Add(time.Hour))
	newer["audio_rel_path"] = "../outside.m4a"
	writeCaptureDay(t, root, "2026-08-20", []map[string]any{older, newer})
	path := filepath.Join(HistoryPath(root), "2026-08-20.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{bad json}\n")
	partial, _ := json.Marshal(captureSource("partial", base.Add(2*time.Hour)))
	_, _ = file.Write(partial) // 无换行，视为 writer 尚未提交
	_ = file.Close()

	items, err := List(context.Background(), root, Query{})
	if err != nil || len(items) != 1 || items[0].ID != "same" || items[0].RecordedAt != newer["recorded_at"] {
		t.Fatalf("bad rows/dedup/tail = %+v, err=%v", items, err)
	}
	if !contains(items[0].Diagnostics, "duplicate_index_entry") {
		t.Fatalf("duplicate diagnostic = %+v", items[0].Diagnostics)
	}
	if items[0].HasAudio || len(items[0].MissingArtifacts) == 0 || items[0].MissingArtifacts[0] != "audio" {
		t.Fatalf("unsafe declared path must be unavailable: %+v", items[0])
	}
}

func TestListMissingHistoryIsEmptyAndUnreadableIndexErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "voice")
	items, err := List(context.Background(), root, Query{})
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("missing source should be empty: %#v err=%v", items, err)
	}
	badRoot := filepath.Join(t.TempDir(), "voice")
	if err := os.MkdirAll(badRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HistoryPath(badRoot), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = List(context.Background(), badRoot, Query{})
	var typed *errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.CodeStorageUnavailable {
		t.Fatalf("existing non-file index should be storage unavailable: %v", err)
	}
}

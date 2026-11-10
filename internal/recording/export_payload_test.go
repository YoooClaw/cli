package recording

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedExportPayloadRecording(t *testing.T, transcript *ResultTranscript, summary *ResultSummary) (*Storage, Entry) {
	t.Helper()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_payload", Metadata{
		Name: "产品评审", CreatedAt: "2026-11-09T10:00:00+08:00", DurationSec: 65.5,
		RecordingType: strPtr(RecordingTypeCall), Location: map[string]any{"city": "杭州"},
	}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if transcript != nil || summary != nil {
		if _, err := HandleRecordingResultWrite(ResultWriteParams{
			RecordingID: "rec_payload", Transcript: transcript, Summary: summary,
		}, storage, testLogger{t}, SyncOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	path := storage.AudioFilePath("rec_payload", "https://example.test/audio.m4a")
	if err := os.WriteFile(path, []byte("payload-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_payload", filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("rec_payload")
	return storage, entry
}

func TestPrepareExportSourceFullPayload(t *testing.T) {
	start := 1500.0
	end := 2500.0
	speaker := 2
	storage, _ := seedExportPayloadRecording(t, &ResultTranscript{
		GeneratedAt: "2026-11-09T10:02:00+08:00",
		Source:      &ResultTranscriptSource{Provider: "asr", TaskID: "task-1", RequestID: "req-1", Status: "done"},
		Title:       "评审结论", Category: "会议", Brief: "简述", Text: "第一段",
		Segments: []ResultTranscriptSegment{{Text: "第一段", StartMS: &start, EndMS: &end, SpeakerID: &speaker}},
	}, &ResultSummary{Markdown: "## 结论\n\n通过"})
	source, err := prepareExportSource(storage, "rec_payload", true)
	if err != nil {
		t.Fatal(err)
	}
	payload := source.payload
	if payload.RecordingID != "rec_payload" || payload.DurationMillis == nil || *payload.DurationMillis != 65000 {
		t.Fatalf("payload=%+v", payload)
	}
	if payload.Recording.RecordingType != RecordingTypeCall || payload.Recording.Location == nil || len(payload.Recording.Markers) != 0 {
		t.Fatalf("recording=%+v", payload.Recording)
	}
	tr := payload.Transcript
	if tr == nil || tr.Text != "第一段" || len(tr.Segments) != 1 || *tr.Segments[0].StartMS != 1500 || *tr.Segments[0].EndMS != 2500 {
		t.Fatalf("transcript=%+v", tr)
	}
	if tr.Source == nil || tr.Source.Provider != "asr" || tr.Source.TaskID != "task-1" || tr.Source.Status != "done" {
		t.Fatalf("source=%+v", tr.Source)
	}
	if payload.Summary == nil || !strings.Contains(payload.Summary.Markdown, "通过") || payload.Summary.Format != "markdown" {
		t.Fatalf("summary=%+v", payload.Summary)
	}
	if !source.contentPresent.Transcript || !source.contentPresent.Summary {
		t.Fatalf("contentPresent=%+v", source.contentPresent)
	}
	if source.fileName != "rec_payload.m4a" || source.mimeType != "audio/mp4" || source.sizeBytes != int64(len("payload-audio")) || source.sha256 == "" {
		t.Fatalf("source=%+v", source)
	}

	// 状态查询用的轻量快照必须与初次导出使用同一 sourceVersion。
	cheap, err := prepareExportSource(storage, "rec_payload", false)
	if err != nil {
		t.Fatal(err)
	}
	if cheap.sourceVersion != source.sourceVersion {
		t.Fatalf("sourceVersion changed between hash and identity snapshots")
	}
	if _, _, err := storage.Rename("rec_payload", "改名"); err != nil {
		t.Fatal(err)
	}
	changed, err := prepareExportSource(storage, "rec_payload", false)
	if err != nil {
		t.Fatal(err)
	}
	if changed.sourceVersion == source.sourceVersion {
		t.Fatal("metadata change did not change sourceVersion")
	}
}

func TestPrepareExportSourceSRTOnly(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_srt", Metadata{Name: "旧录音", CreatedAt: "2026-11-09T10:00:00+08:00"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	audio := storage.AudioFilePath("rec_srt", "https://example.test/audio.ogg")
	if err := os.WriteFile(audio, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_srt", filepath.Base(audio)); err != nil {
		t.Fatal(err)
	}
	srt := "1\n00:00:01,000 --> 00:00:02,500\n第一句\n\n2\n00:00:03,000 --> 00:00:04,000\n第二句\n"
	if err := os.WriteFile(filepath.Join(storage.dir, "transcripts", "rec_srt.srt"), []byte(srt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.updateEntry("rec_srt", func(entry *Entry) { entry.SrtFile = "transcripts/rec_srt.srt" }); err != nil {
		t.Fatal(err)
	}
	source, err := prepareExportSource(storage, "rec_srt", false)
	if err != nil {
		t.Fatal(err)
	}
	tr := source.payload.Transcript
	if tr == nil || len(tr.Segments) != 2 || tr.Text != "第一句\n第二句" {
		t.Fatalf("transcript=%+v", tr)
	}
	if *tr.Segments[0].StartMS != 1000 || *tr.Segments[0].EndMS != 2500 {
		t.Fatalf("segment=%+v", tr.Segments[0])
	}
	if !source.contentPresent.Transcript || source.payload.Summary != nil {
		t.Fatalf("contentPresent=%+v summary=%+v", source.contentPresent, source.payload.Summary)
	}
}

func TestPrepareExportSourceHeaderOnlyMarkdownIsNotPresent(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_md", Metadata{Name: "只有标题", CreatedAt: "2026-11-09T10:00:00+08:00"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	audio := storage.AudioFilePath("rec_md", "https://example.test/audio.ogg")
	if err := os.WriteFile(audio, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_md", filepath.Base(audio)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage.dir, "transcripts", "rec_md.md"), []byte("# 只有标题\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage.dir, "summaries", "rec_md.md"), []byte("## 只有标题\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.updateEntry("rec_md", func(entry *Entry) {
		entry.TranscriptFile, entry.SummaryFile = "transcripts/rec_md.md", "summaries/rec_md.md"
	}); err != nil {
		t.Fatal(err)
	}
	source, err := prepareExportSource(storage, "rec_md", false)
	if err != nil {
		t.Fatal(err)
	}
	if source.contentPresent.Transcript || source.contentPresent.Summary {
		t.Fatalf("contentPresent=%+v", source.contentPresent)
	}
	if source.payload.Transcript == nil || source.payload.Summary == nil {
		t.Fatalf("objects must still be carried: %+v", source.payload)
	}
}

func TestPrepareExportSourceErrors(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_missing", Metadata{Name: "x"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareExportSource(storage, "rec_missing", false); !errors.Is(err, ErrExportSourceNotFound) {
		t.Fatalf("err=%v", err)
	}
	audio := storage.AudioFilePath("rec_missing", "https://example.test/audio.ogg")
	if err := os.WriteFile(audio, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_missing", filepath.Base(audio)); err != nil {
		t.Fatal(err)
	}
	if err := storage.updateEntry("rec_missing", func(entry *Entry) { entry.AudioFile = "audio/escape.txt" }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage.dir, "audio", "escape.txt"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareExportSource(storage, "rec_missing", false); err == nil || !strings.Contains(err.Error(), "unsupported recording audio extension") {
		t.Fatalf("err=%v", err)
	}
	if _, err := prepareExportSource(storage, "nope", false); !errors.Is(err, ErrExportSourceNotFound) {
		t.Fatalf("err=%v", err)
	}
}

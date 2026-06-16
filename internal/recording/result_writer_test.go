package recording

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newResultStorage(t *testing.T) *Storage {
	t.Helper()
	storage := NewStorage(filepath.Join(t.TempDir(), "recordings"), testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	return storage
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

func TestResultWriteTranscriptAndSummary(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_1", Metadata{Name: "会议", CreatedAt: "2026-06-09T20:30:00+08:00", OssAudioURL: "https://oss/rec_1.m4a"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.UpdateStatus("rec_1", StatusSynced); err == nil {
		// syncing_openclaw -> synced is valid; ignore error result
	}

	var events []StatusEvent
	result, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_1",
		Transcript: &ResultTranscript{
			GeneratedAt: "2026-06-09T20:25:00+08:00",
			Source:      &ResultTranscriptSource{Provider: "model-proxy", TaskID: "asr-1", Status: "SUCCEEDED"},
			Title:       "产品方案讨论",
			Category:    "meeting",
			Brief:       "讨论新链路。",
			Segments: []ResultTranscriptSegment{
				{Text: "第一段。", StartMS: ptrF(0), EndMS: ptrF(4200), SpeakerID: ptrI(1)},
				{Text: "第二段。", StartMS: ptrF(4200), EndMS: ptrF(8000), SpeakerID: ptrI(2)},
			},
		},
		Summary: &ResultSummary{Markdown: "# 总结\n\n- App 写入结果。"},
	}, storage, testLogger{t}, SyncOptions{
		NotifyStatus: func(e StatusEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.OK || result.TransferStatus != StatusTranscribed {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Stored.TranscriptDataFile == "" || result.Stored.TranscriptFile == "" || result.Stored.SummaryFile == "" {
		t.Fatalf("missing stored files: %+v", result.Stored)
	}

	// transcript-data 落盘并标记 delivery=result-write
	raw, err := os.ReadFile(filepath.Join(storage.dir, result.Stored.TranscriptDataFile))
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := ParseTranscriptDocument(raw)
	if !ok {
		t.Fatalf("invalid transcript-data: %s", raw)
	}
	if doc.Source.Provider != "model-proxy" || doc.Source.Delivery != "result-write" {
		t.Fatalf("unexpected source: %+v", doc.Source)
	}
	if doc.GeneratedAt != "2026-06-09T20:25:00+08:00" {
		t.Fatalf("generatedAt not preserved: %s", doc.GeneratedAt)
	}
	if len(doc.Normalized.Segments) != 2 || doc.Normalized.Summary != "讨论新链路。" {
		t.Fatalf("unexpected normalized: %+v", doc.Normalized)
	}

	summary, err := os.ReadFile(filepath.Join(storage.dir, result.Stored.SummaryFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "App 写入结果") {
		t.Fatalf("unexpected summary: %s", summary)
	}

	entry, _ := storage.FindByID("rec_1")
	if entry.Title != "产品方案讨论" {
		t.Fatalf("title not set: %q", entry.Title)
	}
	if len(events) == 0 || events[len(events)-1].TransferStatus != StatusTranscribed {
		t.Fatalf("missing transcribed status event: %+v", events)
	}
}

func TestResultWriteUpsertsWhenMissing(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	result, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_new",
		Summary:     &ResultSummary{Markdown: "# 总结\n\n仅总结。"},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.OK || result.TransferStatus != StatusTranscribed {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, ok := storage.FindByID("rec_new"); !ok {
		t.Fatal("recording not upserted")
	}
}

func TestResultWriteRejectsEmptyPayload(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	if _, err := HandleRecordingResultWrite(ResultWriteParams{RecordingID: "rec_1"}, storage, testLogger{t}, SyncOptions{}); err == nil {
		t.Fatal("expected error for empty payload")
	}
	if _, err := HandleRecordingResultWrite(ResultWriteParams{Summary: &ResultSummary{Markdown: "x"}}, storage, testLogger{t}, SyncOptions{}); err == nil {
		t.Fatal("expected error for missing recordingId")
	}
}

func TestResultWriteStructuredSummary(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	_, _ = storage.Ingest("rec_1", Metadata{Name: "x", CreatedAt: "t", OssAudioURL: "u"}, "")
	result, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_1",
		Summary:     &ResultSummary{Structured: json.RawMessage(`{"decisions":["保持 sync 不变"],"todos":["新增 result.write"]}`)},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(storage.dir, result.Stored.SummaryFile))
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	if !strings.Contains(md, "## 结论") || !strings.Contains(md, "- 保持 sync 不变") || !strings.Contains(md, "## 待办") {
		t.Fatalf("unexpected structured summary markdown: %s", md)
	}
}

func TestResultWriteDownloadsOssAudio(t *testing.T) {
	t.Parallel()
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("final-audio"))
	}))
	defer oss.Close()

	storage := newResultStorage(t)
	_, _ = storage.Ingest("rec_1", Metadata{Name: "x", CreatedAt: "t", OssAudioURL: ""}, "")

	var mu sync.Mutex
	var events []StatusEvent
	result, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_1",
		OssURL:      oss.URL + "/final.m4a",
		Transcript:  &ResultTranscript{Title: "标题", Text: "正文"},
	}, storage, testLogger{t}, SyncOptions{
		NotifyStatus:    func(e StatusEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
		DownloadOptions: DownloadOptions{MaxRetries: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = result

	waitFor(t, 2*time.Second, func() bool {
		entry, ok := storage.FindByID("rec_1")
		return ok && entry.AudioFile != ""
	})
	entry, _ := storage.FindByID("rec_1")
	audioBytes, err := os.ReadFile(filepath.Join(storage.dir, entry.AudioFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(audioBytes) != "final-audio" {
		t.Fatalf("unexpected audio content: %s", audioBytes)
	}
}

func TestResultWriteOverwritesPreviousFiles(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	_, _ = storage.Ingest("rec_1", Metadata{Name: "x", CreatedAt: "t", OssAudioURL: "u"}, "")

	first, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_1",
		Transcript:  &ResultTranscript{Title: "旧标题", Text: "旧正文"},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}

	second, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_1",
		Transcript:  &ResultTranscript{Title: "新标题", Text: "新正文"},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// 标题变化 → 旧 transcript markdown 文件被删除
	if first.Stored.TranscriptFile == second.Stored.TranscriptFile {
		t.Fatal("expected transcript filename to change with title")
	}
	if _, err := os.Stat(filepath.Join(storage.dir, first.Stored.TranscriptFile)); !os.IsNotExist(err) {
		t.Fatalf("old transcript file should be removed, err=%v", err)
	}
	entry, _ := storage.FindByID("rec_1")
	if entry.Title != "新标题" {
		t.Fatalf("title not overwritten: %q", entry.Title)
	}
}

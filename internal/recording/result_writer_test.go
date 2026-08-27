package recording

import (
	"encoding/json"
	"fmt"
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
	t.Cleanup(func() { _ = storage.Close() })
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

	// 转写和摘要落在各自目录，但用同一个 <时间>_<标题>_<ID>.md 文件名：
	// 摘要写入时从索引读回 transcript 刚落下的标题。
	stamp := time.Date(2026, 6, 9, 20, 30, 0, 0, time.FixedZone("CST", 8*3600)).
		In(time.Local).Format("2006010215")
	wantName := stamp + "_产品方案讨论_rec_1.md"
	if got := filepath.Base(result.Stored.TranscriptFile); got != wantName {
		t.Fatalf("transcript filename = %q, want %q", got, wantName)
	}
	if got := filepath.Base(result.Stored.SummaryFile); got != wantName {
		t.Fatalf("summary filename = %q, want %q", got, wantName)
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

func TestResultWriteReadsRenamedSummaryFile(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_2", Metadata{Name: "会议", CreatedAt: "2026-06-09T20:30:00+08:00"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_2",
		Summary:     &ResultSummary{Markdown: "# 总结\n\n- 摘要正文。"},
	}, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	// 摘要文件改名（磁盘 + 索引），文件名不再是 ID 的纯函数。
	renamed := "rec_2_产品方案讨论.md"
	entry, _ := storage.FindByID("rec_2")
	if err := os.Rename(filepath.Join(storage.dir, entry.SummaryFile), filepath.Join(storage.SummariesDir(), renamed)); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetSummaryFile("rec_2", renamed); err != nil {
		t.Fatal(err)
	}

	// 第二次只写 transcript，摘要正文要从索引记录的路径读回，而不是按 ID 重算。
	var events []StatusEvent
	if _, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_2",
		Transcript: &ResultTranscript{
			Title:    "产品方案讨论",
			Segments: []ResultTranscriptSegment{{Text: "第一段。", StartMS: ptrF(0), EndMS: ptrF(1000)}},
		},
	}, storage, testLogger{t}, SyncOptions{
		NotifyStatus: func(e StatusEvent) { events = append(events, e) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("missing status event")
	}
	last := events[len(events)-1]
	if !strings.Contains(last.Summary, "摘要正文") {
		t.Fatalf("summary not read from renamed file: %q", last.Summary)
	}
	if last.SummaryFile != summariesDirName+"/"+renamed {
		t.Fatalf("unexpected summaryFile: %q", last.SummaryFile)
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

func TestResultWriteUsesRecordingCreatedAtInsteadOfTranscriptGeneratedAt(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	const recordingCreatedAt = "2026-07-29T08:58:00+08:00"
	const transcriptGeneratedAt = "2026-07-28T23:59:00+08:00"

	_, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_with_metadata",
		Recording: &Metadata{
			Name:            "今天的录音",
			DurationSec:     42,
			FileSizeDisplay: "2 kB",
			CreatedAt:       recordingCreatedAt,
		},
		Transcript: &ResultTranscript{
			Title: "转写标题", GeneratedAt: transcriptGeneratedAt, Text: "正文",
		},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := storage.FindByID("rec_with_metadata")
	if !ok {
		t.Fatal("recording not upserted")
	}
	if entry.Metadata.CreatedAt != recordingCreatedAt {
		t.Fatalf("created_at = %q, want recording time %q", entry.Metadata.CreatedAt, recordingCreatedAt)
	}
	if entry.Metadata.Name != "今天的录音" || entry.Metadata.DurationSec != 42 ||
		entry.Metadata.DurationDisplay != "42s" || entry.Metadata.FileSizeDisplay != "2 kB" {
		t.Fatalf("recording metadata was not preserved: %+v", entry.Metadata)
	}
}

func TestResultWriteTruncatesTopLevelDurationMillis(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		durationMillis float64
		wantSeconds    float64
	}{
		{"below one second", 999, 0},
		{"one second", 1_000, 1},
		{"fractional seconds", 13_480, 13},
		{"half second still truncates", 13_500, 13},
		{"observed app payload", 16_780, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := newResultStorage(t)
			payload := fmt.Sprintf(
				`{"recordingId":"duration-test","durationMillis":%v,"summary":{"markdown":"x"}}`,
				tt.durationMillis,
			)
			var params ResultWriteParams
			if err := json.Unmarshal([]byte(payload), &params); err != nil {
				t.Fatal(err)
			}
			if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
				t.Fatal(err)
			}
			entry, _ := storage.FindByID("duration-test")
			if entry.Metadata.DurationSec != tt.wantSeconds {
				t.Fatalf("duration_sec = %v, want %v", entry.Metadata.DurationSec, tt.wantSeconds)
			}
			if entry.Metadata.DurationDisplay != FormatDurationDisplay(tt.wantSeconds) {
				t.Fatalf("duration_display = %q, want %q",
					entry.Metadata.DurationDisplay, FormatDurationDisplay(tt.wantSeconds))
			}
		})
	}
}

func TestResultWriteWithoutRecordingTimeFallsBackToIngestedAt(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	const transcriptGeneratedAt = "2026-07-28T23:59:00+08:00"
	before := time.Now().Add(-time.Second)

	_, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_without_metadata",
		Transcript: &ResultTranscript{
			GeneratedAt: transcriptGeneratedAt, Title: "标题", Text: "正文",
		},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(time.Second)

	entry, _ := storage.FindByID("rec_without_metadata")
	if entry.Metadata.CreatedAt == transcriptGeneratedAt {
		t.Fatal("transcript generatedAt must not be used as recording created_at")
	}
	createdAt, ok := parseRecordingTime(entry.Metadata.CreatedAt)
	if !ok || createdAt.Before(before) || createdAt.After(after) {
		t.Fatalf("created_at should fall back to ingest time, got %q", entry.Metadata.CreatedAt)
	}
}

func TestResultWriteRepairsExistingPlaceholderMetadata(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_existing", Metadata{
		Name: "旧占位", CreatedAt: "2026-07-28T09:00:00+08:00",
	}, ""); err != nil {
		t.Fatal(err)
	}

	_, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_existing",
		Recording: &Metadata{
			Name: "真实名称", CreatedAt: "2026-07-29T10:00:00+08:00", DurationSec: 60,
		},
		Summary: &ResultSummary{Markdown: "x"},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}

	entry, _ := storage.FindByID("rec_existing")
	if entry.Metadata.Name != "真实名称" ||
		entry.Metadata.CreatedAt != "2026-07-29T10:00:00+08:00" ||
		entry.Metadata.DurationSec != 60 {
		t.Fatalf("existing placeholder metadata was not repaired: %+v", entry.Metadata)
	}
}

func TestResultWriteAcceptsTopLevelCreatedAtAliases(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"camel", `{"recordingId":"r1","createdAt":"2026-07-29T09:00:00+08:00","summary":{"markdown":"x"}}`, "2026-07-29T09:00:00+08:00"},
		{"snake", `{"recordingId":"r1","created_at":"2026-07-29T09:01:00+08:00","summary":{"markdown":"x"}}`, "2026-07-29T09:01:00+08:00"},
		{"nested", `{"recordingId":"r1","recording":{"name":"n","created_at":"2026-07-29T09:02:00+08:00"},"summary":{"markdown":"x"}}`, "2026-07-29T09:02:00+08:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := newResultStorage(t)
			var params ResultWriteParams
			if err := json.Unmarshal([]byte(tc.payload), &params); err != nil {
				t.Fatal(err)
			}
			if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
				t.Fatal(err)
			}
			entry, _ := storage.FindByID("r1")
			if entry.Metadata.CreatedAt != tc.want {
				t.Fatalf("created_at = %q, want %q", entry.Metadata.CreatedAt, tc.want)
			}
		})
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
	if entry.AudioStatus != AudioStatusDownloaded || entry.LastError != "" {
		t.Fatalf("unexpected audio state after success: %+v", entry)
	}
	if entry.Metadata.FileSizeDisplay != "11 B" {
		t.Fatalf("file_size_display = %q, want %q", entry.Metadata.FileSizeDisplay, "11 B")
	}
	rawIndex, err := os.ReadFile(storage.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawIndex), "file_size_bytes") ||
		!strings.Contains(string(rawIndex), `"file_size_display": "11 B"`) ||
		!strings.Contains(string(rawIndex), `"duration_display": "0s"`) {
		t.Fatalf("index contains unexpected file-size schema: %s", rawIndex)
	}
}

func TestResultWritePersistsAudioFailureAndLatestURL(t *testing.T) {
	t.Parallel()
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer oss.Close()

	storage := newResultStorage(t)
	_, _ = storage.Ingest("rec_failed", Metadata{
		Name: "x", CreatedAt: "t", OssAudioURL: "https://old.invalid/audio.ogg",
	}, "")

	var mu sync.Mutex
	var events []StatusEvent
	result, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_failed",
		OssURL:      oss.URL + "/latest.ogg",
		Transcript:  &ResultTranscript{Title: "标题", Text: "正文"},
	}, storage, testLogger{t}, SyncOptions{
		NotifyStatus: func(e StatusEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
		DownloadOptions: DownloadOptions{MaxRetries: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AudioStatus != AudioStatusPending {
		t.Fatalf("result should report accepted/pending audio: %+v", result)
	}

	waitFor(t, 2*time.Second, func() bool {
		entry, ok := storage.FindByID("rec_failed")
		return ok && entry.AudioStatus == AudioStatusFailed
	})
	entry, _ := storage.FindByID("rec_failed")
	if entry.Metadata.OssAudioURL != oss.URL+"/latest.ogg" {
		t.Fatalf("latest OSS URL was not persisted: %+v", entry.Metadata)
	}
	if entry.Status != StatusTranscribed || entry.LastError == "" || entry.AudioFile != "" {
		t.Fatalf("audio failure should remain observable: %+v", entry)
	}

	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 2 && events[len(events)-1].AudioStatus == AudioStatusFailed
	})
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("missing pending/failure events: %+v", events)
	}
	last := events[len(events)-1]
	if last.AudioStatus != AudioStatusFailed || last.Error == "" {
		t.Fatalf("failure event was masked: %+v", last)
	}
}

func TestRecoverMissingResultAudio(t *testing.T) {
	t.Parallel()
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("recovered-audio"))
	}))
	defer oss.Close()

	storage := newResultStorage(t)
	_, _ = storage.Ingest("rec_recover", Metadata{
		Name: "x", CreatedAt: "t", OssAudioURL: oss.URL + "/recover.ogg",
	}, "")

	if count := RecoverMissingResultAudio(storage, testLogger{t}, SyncOptions{
		DownloadOptions: DownloadOptions{MaxRetries: 1},
	}); count != 1 {
		t.Fatalf("recovery count=%d, want 1", count)
	}
	waitFor(t, 2*time.Second, func() bool {
		entry, ok := storage.FindByID("rec_recover")
		return ok && entry.AudioStatus == AudioStatusDownloaded && entry.AudioFile != ""
	})
	entry, _ := storage.FindByID("rec_recover")
	data, err := os.ReadFile(filepath.Join(storage.dir, entry.AudioFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "recovered-audio" {
		t.Fatalf("unexpected recovered audio: %q", data)
	}
}

func TestResultWriteFailedReplacementKeepsPreviousAudio(t *testing.T) {
	t.Parallel()
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good.ogg" {
			_, _ = w.Write([]byte("known-good-audio"))
			return
		}
		http.NotFound(w, r)
	}))
	defer oss.Close()

	storage := newResultStorage(t)
	_, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_replace",
		OssURL:      oss.URL + "/good.ogg",
		Transcript:  &ResultTranscript{Title: "标题", Text: "正文"},
	}, storage, testLogger{t}, SyncOptions{
		DownloadOptions: DownloadOptions{MaxRetries: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		entry, ok := storage.FindByID("rec_replace")
		return ok && entry.AudioStatus == AudioStatusDownloaded
	})
	before, _ := storage.FindByID("rec_replace")
	beforePath := filepath.Join(storage.dir, before.AudioFile)

	_, err = HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_replace",
		OssURL:      oss.URL + "/missing.ogg",
		Transcript:  &ResultTranscript{Title: "标题", Text: "更新正文"},
	}, storage, testLogger{t}, SyncOptions{
		DownloadOptions: DownloadOptions{MaxRetries: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		entry, ok := storage.FindByID("rec_replace")
		return ok && entry.AudioStatus == AudioStatusFailed
	})
	after, _ := storage.FindByID("rec_replace")
	if after.AudioFile != before.AudioFile || after.AudioSourceURL != oss.URL+"/good.ogg" {
		t.Fatalf("failed replacement changed known-good audio reference: before=%+v after=%+v", before, after)
	}
	data, err := os.ReadFile(beforePath)
	if err != nil {
		t.Fatalf("known-good audio was removed: %v", err)
	}
	if string(data) != "known-good-audio" {
		t.Fatalf("known-good audio was overwritten: %q", data)
	}
	if missing := storage.ListMissingAudio(); len(missing) != 1 || missing[0].ID != "rec_replace" {
		t.Fatalf("failed replacement was not queued for recovery: %+v", missing)
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

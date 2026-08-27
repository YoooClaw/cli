package recording

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func revisionPtr(value int64) *int64 { return &value }

func orderedMetadata(url string) *Metadata {
	return &Metadata{
		Name:        "有序录音",
		DurationSec: 12,
		CreatedAt:   "2026-08-27T11:00:00+08:00",
		OssAudioURL: url,
		Markers:     []Marker{},
	}
}

func waitForAudioStatus(t *testing.T, storage *Storage, recordingID, status string) Entry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entry, ok := storage.FindByID(recordingID)
		if ok && entry.AudioStatus == status {
			return entry
		}
		time.Sleep(10 * time.Millisecond)
	}
	entry, _ := storage.FindByID(recordingID)
	t.Fatalf("audio status = %q, want %q", entry.AudioStatus, status)
	return Entry{}
}

func TestOrderedAudioOnlyThenFullResult(t *testing.T) {
	storage := newResultStorage(t)
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer audio.Close()

	result, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "ordered-1", WriteRevision: revisionPtr(1), OssURL: audio.URL + "/a.m4a",
		Recording: orderedMetadata(audio.URL + "/a.m4a"),
	}, storage, testLogger{t}, SyncOptions{DownloadOptions: DownloadOptions{MaxRetries: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if result.WriteRevision == nil || *result.WriteRevision != 1 || result.TransferStatus != "syncing_openclaw" {
		t.Fatalf("unexpected audio-only result: %+v", result)
	}
	entry := waitForAudioStatus(t, storage, "ordered-1", AudioStatusDownloaded)
	if entry.Status != StatusSynced || entry.AudioFile == "" {
		t.Fatalf("unexpected downloaded entry: %+v", entry)
	}

	result, err = HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "ordered-1", WriteRevision: revisionPtr(2), OssURL: audio.URL + "/changed-url.m4a",
		Recording:  orderedMetadata(audio.URL + "/changed-url.m4a"),
		Transcript: &ResultTranscript{Text: "正文", Title: "新标题"},
		Summary:    &ResultSummary{Markdown: "总结"},
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.TransferStatus != StatusTranscribed || result.Stored.TranscriptDataFile == "" || result.Stored.SummaryFile == "" {
		t.Fatalf("unexpected full result: %+v", result)
	}
	updated, _ := storage.FindByID("ordered-1")
	if updated.AudioFile != entry.AudioFile || updated.AudioStatus != AudioStatusDownloaded {
		t.Fatalf("existing immutable audio was not reused: before=%+v after=%+v", entry, updated)
	}
	for _, relative := range []string{updated.TranscriptDataFile, updated.TranscriptFile, updated.SummaryFile} {
		info, statErr := os.Stat(filepath.Join(storage.dir, relative))
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("missing ordered artifact %q: %v", relative, statErr)
		}
	}
}

func TestOrderedSameRevisionIsIdempotent(t *testing.T) {
	storage := newResultStorage(t)
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("audio")) }))
	defer audio.Close()
	url := audio.URL + "/audio.m4a"
	params := ResultWriteParams{
		RecordingID: "ordered-2", WriteRevision: revisionPtr(4), OssURL: url,
		Recording:  orderedMetadata(url),
		Transcript: &ResultTranscript{Text: "固定正文"},
		Summary:    &ResultSummary{Markdown: "固定总结"},
	}
	first, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Stored != second.Stored {
		t.Fatalf("idempotent retry created new artifacts: first=%+v second=%+v", first.Stored, second.Stored)
	}
}

func TestOrderedRejectsStaleAndLegacyMixing(t *testing.T) {
	storage := newResultStorage(t)
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("audio")) }))
	defer audio.Close()
	url := audio.URL + "/audio.m4a"
	_, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "ordered-3", WriteRevision: revisionPtr(2), OssURL: url,
		Recording: orderedMetadata(url),
	}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "ordered-3", WriteRevision: revisionPtr(1), OssURL: url,
		Recording: orderedMetadata(url),
	}, storage, testLogger{t}, SyncOptions{})
	var writeErr *WriteError
	if !errors.As(err, &writeErr) || writeErr.Code != "STALE_WRITE" {
		t.Fatalf("stale error = %#v", err)
	}
	_, err = HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "ordered-3", Transcript: &ResultTranscript{Text: "legacy"},
	}, storage, testLogger{t}, SyncOptions{})
	if !errors.As(err, &writeErr) || writeErr.Code != "REVISION_REQUIRED" {
		t.Fatalf("legacy mixing error = %#v", err)
	}
}

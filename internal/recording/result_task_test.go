package recording

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResultTaskIDPersistsAndDoesNotChangeDedup(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_task", Metadata{}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	path := storage.AudioFilePath("rec_task", "https://example.test/audio.ogg")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_task", filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	task := "upload-task"
	params := ResultWriteParams{RecordingID: "rec_task", OssURL: "https://example.test/new.ogg", OssTaskID: &task, Summary: &ResultSummary{Markdown: "test"}}
	var event StatusEvent
	result, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{NotifyStatus: func(e StatusEvent) { event = e }})
	if err != nil || result.AudioStatus != AudioStatusDownloaded || result.OssTaskID != task || event.OssTaskID != task {
		t.Fatalf("result=%+v event=%+v err=%v", result, event, err)
	}
	restored := NewStorage(storage.dir, testLogger{t})
	if err := restored.Init(); err != nil {
		t.Fatal(err)
	}
	entry, _ := restored.FindByID("rec_task")
	if entry.OssTaskID != task {
		t.Fatal("task not persisted")
	}
	params.OssURL = ""
	params.OssTaskID = nil
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	entry, _ = storage.FindByID("rec_task")
	if entry.OssTaskID != task {
		t.Fatal("text-only update cleared task")
	}
	params.OssURL = "https://example.test/old-app.ogg"
	result, err = HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{})
	if err != nil || result.OssTaskID != "" || result.AudioStatus != AudioStatusDownloaded {
		t.Fatalf("legacy update=%+v err=%v", result, err)
	}
}
func TestResultRejectsInvalidTaskBeforeWriting(t *testing.T) {
	storage := newResultStorage(t)
	task := " "
	_, err := HandleRecordingResultWrite(ResultWriteParams{RecordingID: "invalid", OssTaskID: &task, Summary: &ResultSummary{Markdown: "test"}}, storage, testLogger{t}, SyncOptions{})
	if err == nil {
		t.Fatal("invalid task accepted")
	}
	if _, ok := storage.FindByID("invalid"); ok {
		t.Fatal("invalid request mutated storage")
	}
}

func TestSupersededRefreshDoesNotFailReplacement(t *testing.T) {
	storage := newResultStorage(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer source.Close()
	if _, err := storage.Ingest("rec_refresh", Metadata{OssAudioURL: source.URL}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetResultAudioPending("rec_refresh", source.URL, "old-task"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		downloadResultAudio("rec_refresh", source.URL, storage, testLogger{t}, SyncOptions{DownloadOptions: DownloadOptions{MaxRetries: 1}, URLRefresher: func(label, task string) func(context.Context) (string, error) {
			return func(context.Context) (string, error) { close(entered); <-release; return source.URL, nil }
		}})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("refresh not called")
	}
	err := storage.SetResultAudioPending("rec_refresh", source.URL, "new-task")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("download did not stop")
	}
	entry, _ := storage.FindByID("rec_refresh")
	if entry.OssTaskID != "new-task" || entry.AudioStatus != AudioStatusPending || entry.LastError != "" {
		t.Fatalf("replacement overwritten: %+v", entry)
	}
}

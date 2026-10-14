package recording

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBackgroundDownloadRemainsBusyAfterRequestReturns(t *testing.T) {
	storage := newResultStorage(t)
	release := make(chan struct{})
	released := false
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release; _, _ = w.Write([]byte("audio")) }))
	defer source.Close()
	defer func() {
		if !released {
			close(release)
		}
	}()
	_, err := HandleRecordingResultWrite(ResultWriteParams{RecordingID: "busy", OssURL: source.URL + "/audio.ogg", Summary: &ResultSummary{Markdown: "test"}}, storage, testLogger{t}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if storage.ActiveDownloads() != 1 {
		t.Fatal("background job not included after request returns")
	}
	close(release)
	released = true
	until := time.Now().Add(2 * time.Second)
	for storage.ActiveDownloads() != 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if storage.ActiveDownloads() != 0 {
		t.Fatal("completed download still busy")
	}
}

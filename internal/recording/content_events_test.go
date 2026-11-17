package recording

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestContentEventPartialResultAndDuplicate(t *testing.T) {
	s := newResultStorage(t)
	var events []bool
	notify := func(e Entry, partial bool) {
		if e.TranscriptDataFile == "" && e.SummaryFile == "" {
			t.Error("unreadable commit")
		}
		events = append(events, partial)
	}
	opts := SyncOptions{OnContentCommit: notify, ClientLabel: "phone"}
	args := ResultWriteParams{RecordingID: "r", Summary: &ResultSummary{Markdown: "summary"}}
	for i := 0; i < 2; i++ {
		if _, e := HandleRecordingResultWrite(args, s, testLogger{t}, opts); e != nil {
			t.Fatal(e)
		}
	}
	if len(events) != 1 || events[0] {
		t.Fatal(events)
	}
	// Transcript commits, then summary write fails: report partial visible content.
	if e := os.RemoveAll(s.SummariesDir()); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(s.SummariesDir(), []byte("blocked"), 0600); e != nil {
		t.Fatal(e)
	}
	args.Transcript = &ResultTranscript{Text: "new transcript", Title: "new title"}
	if _, e := HandleRecordingResultWrite(args, s, testLogger{t}, opts); e == nil {
		t.Fatal("expected summary failure")
	}
	if len(events) != 2 || !events[1] {
		t.Fatal(events)
	}
}
func TestBackgroundTranscriptionNotifiesOnlyCommittedContent(t *testing.T) {
	t.Setenv("OPENCLAW_ASR_POLL_INTERVAL_MS", "1")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit-task" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"taskId": "one", "status": "RUNNING"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"taskId": "one", "status": "SUCCEEDED", "recordResult": map[string]any{"sourceText": "transcript", "summaryResult": "summary", "title": "title"}}})
	}))
	defer api.Close()
	s := newResultStorage(t)
	_, e := s.Ingest("asr", Metadata{Name: "voice", CreatedAt: "2026-10-20T00:00:00Z", OssAudioURL: "https://oss.invalid/asr.ogg"}, "phone")
	if e != nil {
		t.Fatal(e)
	}
	if err := os.WriteFile(s.AudioFilePath("asr", "https://oss.invalid/asr.ogg"), []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := AsrConfig{Mode: "api", API: &AsrAPIConfig{APIKey: "test", Endpoint: api.URL + "/submit-task"}}
	var content, statuses int
	e = TriggerTranscription("asr", s, cfg, testLogger{t}, SyncOptions{NotifyStatus: func(StatusEvent) { statuses++ }, OnContentCommit: func(entry Entry, partial bool) {
		content++
		if partial || entry.ClientLabel != "phone" {
			t.Error(entry, partial)
		}
		for _, name := range []string{entry.TranscriptDataFile, entry.TranscriptFile, entry.SummaryFile} {
			if name == "" {
				t.Error("missing file")
			}
			if _, e := os.ReadFile(filepath.Join(s.dir, name)); e != nil {
				t.Error(e)
			}
		}
	}})
	if e != nil || content != 1 || statuses < 2 {
		t.Fatal(e, content, statuses)
	}
}

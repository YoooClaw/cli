package recording

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type testLogger struct {
	t *testing.T
}

func (l testLogger) Info(msg string)  { l.t.Log("[INFO] " + msg) }
func (l testLogger) Warn(msg string)  { l.t.Log("[WARN] " + msg) }
func (l testLogger) Error(msg string) { l.t.Log("[ERROR] " + msg) }

func TestHandleRecordingSyncDownloadsFiles(t *testing.T) {
	t.Parallel()
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rec.ogg":
			_, _ = w.Write([]byte("audio-bytes"))
		case "/rec.srt":
			_, _ = w.Write([]byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oss.Close()

	storage := NewStorage(filepath.Join(t.TempDir(), "recordings"), testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	var eventsMu sync.Mutex
	var events []StatusEvent
	result := HandleRecordingSyncWithClientLabel("rec_1", Metadata{
		Name: "测试录音", DurationSec: 1, FileSizeBytes: 12,
		CreatedAt:   "2026-06-04T17:16:50+08:00",
		OssAudioURL: oss.URL + "/rec.ogg",
		OssSrtURL:   oss.URL + "/rec.srt",
		Markers:     []Marker{{Index: 0, TimestampMS: 500}},
	}, "phone-a", storage, nil, testLogger{t}, SyncOptions{
		NotifyStatus: func(event StatusEvent) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, event)
		},
		DownloadOptions: DownloadOptions{MaxRetries: 1},
	})
	if !result.OK || result.TransferStatus != StatusSyncingOpenClaw {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	waitFor(t, time.Second, func() bool {
		entry, ok := storage.FindByID("rec_1")
		return ok && entry.Status == StatusSynced && entry.AudioFile == "audio/rec_1.ogg" && entry.SrtFile == "audio/rec_1.srt"
	})
	entry, _ := storage.FindByID("rec_1")
	if entry.ClientLabel != "phone-a" {
		t.Fatalf("client label mismatch: %q", entry.ClientLabel)
	}
	audio, err := os.ReadFile(filepath.Join(storage.AudioDir(), "rec_1.ogg"))
	if err != nil || string(audio) != "audio-bytes" {
		t.Fatalf("audio file mismatch: %q err=%v", string(audio), err)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) == 0 || events[len(events)-1].TransferStatus != StatusSynced {
		t.Fatalf("missing synced event: %+v", events)
	}
}

func TestRunTranscriptionWorkflowWithModelProxy(t *testing.T) {
	t.Setenv("OPENCLAW_ASR_POLL_INTERVAL_MS", "1")
	var sawSubmit bool
	modelProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/submit-task":
			if r.Header.Get("X-Api-Key-Id") != "test-key" {
				t.Fatalf("missing api key header")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["audioOssUrl"] != "https://oss.invalid/rec_asr.ogg" {
				t.Fatalf("unexpected submit body: %+v", body)
			}
			sawSubmit = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"taskId": "task1", "status": "RUNNING", "requestId": "req1"},
			})
		case "/query-task-result/task1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"taskId": "task1", "status": "SUCCEEDED", "requestId": "req2",
					"recordResult": map[string]any{
						"sourceTextList": []map[string]any{{"content": "你好", "speakerId": 1, "startTime": 0, "endTime": 1000}},
						"summaryResult":  "摘要正文",
						"title":          "测试标题",
						"category":       "meeting",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer modelProxy.Close()

	storage := NewStorage(filepath.Join(t.TempDir(), "recordings"), testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	audioPath := storage.AudioFilePath("rec_asr", "https://oss.invalid/rec_asr.ogg")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := RunTranscriptionWorkflow(storage, Entry{
		ID: "rec_asr",
		Metadata: Metadata{
			Name: "ASR 录音", DurationSec: 1, CreatedAt: "2026-06-04T17:16:50+08:00",
			OssAudioURL: "https://oss.invalid/rec_asr.ogg",
		},
	}, AsrConfig{Mode: "api", API: &AsrAPIConfig{APIKey: "test-key", Endpoint: modelProxy.URL + "/submit-task"}}, testLogger{t})
	if !result.OK {
		t.Fatalf("workflow failed: %+v", result)
	}
	if !sawSubmit {
		t.Fatal("submit endpoint was not called")
	}
	raw, err := os.ReadFile(filepath.Join(storage.TranscriptDataDir(), result.TranscriptDataFilename))
	if err != nil {
		t.Fatal(err)
	}
	var doc TranscriptDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Normalized.Title != "测试标题" || doc.Normalized.Text != "你好" || len(doc.Normalized.Segments) != 1 {
		t.Fatalf("unexpected transcript doc: %+v", doc.Normalized)
	}
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

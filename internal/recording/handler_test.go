package recording

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testLogger struct {
	t *testing.T
}

func (l testLogger) Info(msg string)  { l.t.Log("[INFO] " + msg) }
func (l testLogger) Warn(msg string)  { l.t.Log("[WARN] " + msg) }
func (l testLogger) Error(msg string) { l.t.Log("[ERROR] " + msg) }

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

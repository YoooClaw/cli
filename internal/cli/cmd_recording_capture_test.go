package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCaptureCLIIndex(t *testing.T, home, id, title string, recorded time.Time, missingTranscript bool) {
	t.Helper()
	voiceRoot := filepath.Join(home, "profiles", "default", "voice")
	history := filepath.Join(voiceRoot, "recordings-jsonl")
	artifactDir := filepath.Join(voiceRoot, "recordings", recorded.Format("2006"), recorded.Format("01"), id)
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	relRoot := filepath.ToSlash(filepath.Join("recordings", recorded.Format("2006"), recorded.Format("01"), id))
	row := map[string]any{
		"id": id, "title": title,
		"audio_rel_path":      relRoot + "/audio.m4a",
		"transcript_rel_path": relRoot + "/transcript.json",
		"summary_rel_path":    relRoot + "/summary.md",
		"recorded_at":         recorded.Format(time.RFC3339), "duration_ms": 12500, "status": "completed",
	}
	line, _ := json.Marshal(row)
	if err := os.WriteFile(filepath.Join(history, recorded.Format("2006-01-02")+".jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audio.m4a", "summary.md"} {
		if err := os.WriteFile(filepath.Join(artifactDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !missingTranscript {
		if err := os.WriteFile(filepath.Join(artifactDir, "transcript.json"), []byte(`{"transcripts":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeHardwareCLIIndex(t *testing.T, home, id string, created time.Time) {
	t.Helper()
	dir := filepath.Join(home, "profiles", "default", "recordings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	index := fmt.Sprintf(`{"recordings":[{"id":%q,"clientLabel":"desk-device","metadata":{"name":"硬件录音","created_at":%q,"duration_sec":20},"status":"transcribed"}]}`, id, created.Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingMergesCaptureAndSmartHardwareSources(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	writeHardwareCLIIndex(t, home, "hardware-id", base)
	writeCaptureCLIIndex(t, home, "capture-id", "输入法会议", base.Add(time.Hour), true)

	out, code := execCLI(t, "recording", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("recording list failed: %s", out)
	}
	result := decode(t, out)
	items := result["recordings"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != "capture-id" {
		t.Fatalf("mixed list must sort globally: %s", out)
	}
	if items[0].(map[string]any)["source_type"] != "capture_app" || items[0].(map[string]any)["source_name"] != "YoooClaw Capture" {
		t.Fatalf("capture source identity missing: %s", out)
	}
	if items[1].(map[string]any)["source_type"] != "smart_hardware" || items[1].(map[string]any)["source_name"] != "YoooClaw 智能硬件" {
		t.Fatalf("hardware source identity missing: %s", out)
	}

	out, code = execCLI(t, "recording", "list", "--source", "capture_app", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("capture source filter failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "list", "--client", "desk-device", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("client filter should apply only to hardware: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "+latest", "--format", "json")
	if code != 0 || decode(t, out)["recording"].(map[string]any)["id"] != "capture-id" {
		t.Fatalf("mixed latest failed: code=%d %s", code, out)
	}

	out, code = execCLI(t, "recording", "status", "capture-id", "--format", "json")
	if code != 0 {
		t.Fatalf("capture status failed: %s", out)
	}
	detail := decode(t, out)["recording"].(map[string]any)
	if detail["has_audio"] != true || detail["has_transcript"] != false || detail["has_summary"] != true {
		t.Fatalf("capture status must use file facts: %+v", detail)
	}
	missing := detail["missing_artifacts"].([]any)
	if len(missing) != 1 || missing[0] != "transcript" || detail["transcript_path"] != nil {
		t.Fatalf("capture missing artifacts = %+v", detail)
	}
	if audioPath, ok := detail["audio_path"].(string); !ok || !filepath.IsAbs(audioPath) {
		t.Fatalf("capture status must return safe absolute artifact paths: %+v", detail)
	}

	out, code = execCLI(t, "recording", "storage-path", "--format", "json")
	sources := decode(t, out)["sources"].(map[string]any)
	if code != 0 || sources["capture_app"] == nil || sources["smart_hardware"] == nil {
		t.Fatalf("recording storage-path must describe both sources: code=%d %s", code, out)
	}
}

func TestRecordingStatusRequiresSourceWhenIDIsAmbiguous(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.Local)
	writeHardwareCLIIndex(t, home, "same-id", base)
	writeCaptureCLIIndex(t, home, "same-id", "同 ID 会议", base.Add(time.Hour), false)

	out, code := execCLI(t, "recording", "status", "same-id", "--format", "json")
	if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
		t.Fatalf("ambiguous status must fail: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "status", "same-id", "--source", "smart_hardware", "--format", "json")
	if code != 0 || decode(t, out)["recording"].(map[string]any)["source_type"] != "smart_hardware" {
		t.Fatalf("explicit source should disambiguate: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "list", "--source", "mobile", "--format", "json")
	if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
		t.Fatalf("invalid source should fail: code=%d %s", code, out)
	}
}

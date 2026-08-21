package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cliVoiceRow(id string, started time.Time, appID, appName, text string) map[string]any {
	return map[string]any{
		"id": 7, "voice_id": id,
		"started_at": started.Format(time.RFC3339), "ended_at": started.Add(5 * time.Second).Format(time.RFC3339),
		"timezone_offset_min": 480, "duration_ms": 4321, "platform": "macos",
		"app_id": appID, "app_name": appName, "window_title": nil,
		"text": text, "language": nil, "char_count": 88,
		"result_status": "success", "audio_rel_path": nil,
	}
}

func createCLIVoiceJSONL(t *testing.T, yoooclawHome string, rowsByDate map[string][]map[string]any) string {
	t.Helper()
	voiceRoot := filepath.Join(yoooclawHome, "profiles", "default", "voice")
	history := filepath.Join(voiceRoot, "audio-jsonl")
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	for date, rows := range rowsByDate {
		data := make([]byte, 0)
		for _, row := range rows {
			line, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, line...)
			data = append(data, '\n')
		}
		if err := os.WriteFile(filepath.Join(history, date+".jsonl"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return voiceRoot
}

func TestVoiceCLIReadsJSONLUsesVoiceIDAndExactAppIdentity(t *testing.T) {
	home := sandbox(t)
	fixedNow := time.Date(2026, 8, 4, 18, 0, 0, 0, time.Local)
	oldNow := voiceQueryNow
	voiceQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { voiceQueryNow = oldNow })
	old := fixedNow.Add(-73 * time.Hour)
	chat := fixedNow.Add(-50 * time.Hour)
	chrome := fixedNow.Add(-26 * time.Hour)
	edge := fixedNow.Add(-time.Hour)
	createCLIVoiceJSONL(t, home, map[string][]map[string]any{
		old.Format("2006-01-02"): {
			cliVoiceRow("voice-old", old, "com.openai.codex", "ChatGPT", "三天以前的内容"),
		},
		chat.Format("2006-01-02"): {
			cliVoiceRow("voice-chat", chat, "com.openai.codex", "ChatGPT", "第一个项目"),
		},
		chrome.Format("2006-01-02"): {
			cliVoiceRow("voice-chrome", chrome, "com.google.Chrome", "Google Chrome", "讨论第二个项目"),
		},
		edge.Format("2006-01-02"): {
			cliVoiceRow("voice-edge", edge, "com.microsoft.edgemac", "Microsoft Edge", "今天的内容"),
		},
	})

	out, code := execCLI(t, "voice", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("voice list failed: %s", out)
	}
	result := decode(t, out)
	if result["total"] != float64(3) || result["default_range_applied"] != true {
		t.Fatalf("unfiltered voice list must use recent 72 hours: %s", out)
	}
	items := result["items"].([]any)
	if items[0].(map[string]any)["id"] != "voice-edge" {
		t.Fatalf("voice list must expose voice_id and sort newest first: %s", out)
	}
	if _, exists := items[0].(map[string]any)["app_id"]; exists {
		t.Fatalf("voice list must not expose app_id: %s", out)
	}

	out, code = execCLI(t, "voice", "list", "--all", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(4) {
		t.Fatalf("voice list --all must bypass default range: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "list", "--app", "浏览器", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(0) {
		t.Fatalf("unobserved aliases must not expand: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "search", "第一个", "--app", "com.openai.codex", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("exact app_id filter failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "search", "三天以前", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("search must cover all history without explicit range: code=%d %s", code, out)
	}

	out, code = execCLI(t, "voice", "apps", "--format", "json")
	if code != 0 {
		t.Fatalf("voice apps failed: %s", out)
	}
	apps := decode(t, out)["apps"].([]any)
	if len(apps) != 3 || decode(t, out)["default_range_applied"] != true {
		t.Fatalf("voice apps should default to the recent week: %s", out)
	}
	for _, rawApp := range apps {
		app := rawApp.(map[string]any)
		if app["app_id"] == nil || app["app_name"] == nil {
			t.Fatalf("voice apps must expose app_id and app_name: %s", out)
		}
	}
	out, code = execCLI(t, "voice", "+latest", "--format", "json")
	if code != 0 || decode(t, out)["voice"].(map[string]any)["id"] != "voice-edge" {
		t.Fatalf("voice +latest failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "show", "voice-chrome", "--format", "json")
	if code != 0 || decode(t, out)["voice"].(map[string]any)["text"] != "讨论第二个项目" {
		t.Fatalf("voice show by voice_id failed: code=%d %s", code, out)
	}

	out, code = execCLI(t, "voice", "list", "--format", "ndjson")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 3 || !strings.Contains(lines[0], `"id":"voice-edge"`) {
		t.Fatalf("voice list NDJSON failed: code=%d %q", code, out)
	}
}

func TestVoiceAppsDefaultsToRecentWeekAndSupportsAll(t *testing.T) {
	home := sandbox(t)
	fixedNow := time.Date(2026, 8, 21, 12, 0, 0, 0, time.Local)
	recent := fixedNow.Add(-24 * time.Hour)
	old := fixedNow.Add(-8 * 24 * time.Hour)
	createCLIVoiceJSONL(t, home, map[string][]map[string]any{
		recent.Format("2006-01-02"): {
			cliVoiceRow("vscode-1", recent, "com.microsoft.VSCode", "Code", "one"),
			cliVoiceRow("vscode-2", recent.Add(time.Minute), "com.microsoft.VSCode", "Code", "two"),
		},
		old.Format("2006-01-02"): {
			cliVoiceRow("old-chrome", old, "com.google.Chrome", "Google Chrome", "old"),
		},
	})
	oldNow := voiceQueryNow
	voiceQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { voiceQueryNow = oldNow })

	out, code := execCLI(t, "voice", "apps", "--format", "json")
	result := decode(t, out)
	if code != 0 || result["total"] != float64(1) || result["default_range_applied"] != true {
		t.Fatalf("default recent-week apps failed: code=%d %s", code, out)
	}
	app := result["apps"].([]any)[0].(map[string]any)
	if app["app_id"] != "com.microsoft.VSCode" || app["app_name"] != "Code" || app["history_count"] != float64(2) {
		t.Fatalf("VS Code identity mapping = %+v", app)
	}
	rangeValue := result["range"].(map[string]any)
	if rangeValue["from"] != fixedNow.Add(-7*24*time.Hour).Format(time.RFC3339Nano) ||
		rangeValue["to"] != fixedNow.Format(time.RFC3339Nano) {
		t.Fatalf("default apps range = %+v", rangeValue)
	}

	out, code = execCLI(t, "voice", "apps", "--all", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(2) {
		t.Fatalf("voice apps --all failed: code=%d %s", code, out)
	}
}

func TestVoiceTodayStorageAndValidation(t *testing.T) {
	home := sandbox(t)
	fixedNow := time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)
	createCLIVoiceJSONL(t, home, map[string][]map[string]any{
		"2026-08-02": {cliVoiceRow("old", fixedNow.Add(-48*time.Hour), "id", "Example", "old")},
		"2026-08-04": {cliVoiceRow("today", fixedNow.Add(-time.Hour), "id", "Example", "today")},
	})
	oldNow := voiceQueryNow
	voiceQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { voiceQueryNow = oldNow })

	out, code := execCLI(t, "voice", "+today", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("voice +today failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "storage-path", "--format", "json")
	storage := decode(t, out)
	if code != 0 || storage["format"] != "daily-jsonl" || storage["history"] != filepath.Join(home, "profiles", "default", "voice", "audio-jsonl") {
		t.Fatalf("voice storage-path failed: code=%d %s", code, out)
	}
	if _, exists := storage["database"]; exists {
		t.Fatalf("storage-path must not expose legacy database: %s", out)
	}
	out, code = execCLI(t, "voice", "--help")
	if code != 0 || strings.Contains(out, "stats") {
		t.Fatalf("voice stats must be removed from help: code=%d %s", code, out)
	}
	root := newRootCmd()
	root.SetArgs([]string{"voice", "stats"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), `unknown command "stats"`) {
		t.Fatalf("removed voice stats must be an unknown command, got %v", err)
	}

	for _, args := range [][]string{
		{"voice", "list", "--limit", "0"},
		{"voice", "list", "--from", "2026-08-05", "--to", "2026-08-04"},
		{"voice", "search", "   "},
		{"voice", "show", "   "},
	} {
		out, code = execCLI(t, args...)
		if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
			t.Fatalf("%v should be invalid: code=%d %s", args, code, out)
		}
	}
}

func TestVoiceCLIMissingJSONLStorage(t *testing.T) {
	home := sandbox(t)
	voiceRoot := filepath.Join(home, "profiles", "default", "voice")
	if err := os.MkdirAll(voiceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(voiceRoot, "voice.sqlite3"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := execCLI(t, "voice", "list", "--format", "json")
	if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_VOICE_STORAGE_NOT_FOUND" {
		t.Fatalf("missing JSONL storage should be explicit: code=%d %s", code, out)
	}
}

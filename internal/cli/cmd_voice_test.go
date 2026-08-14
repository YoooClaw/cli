package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func createCLIVoiceDatabase(t *testing.T, yoooclawHome string) *sql.DB {
	t.Helper()
	dir := filepath.Join(yoooclawHome, "profiles", "default", "voice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "voice.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE voice_history (
id INTEGER PRIMARY KEY, started_at_ms INTEGER NOT NULL, ended_at_ms INTEGER NOT NULL,
timezone_offset_min INTEGER NOT NULL, duration_ms INTEGER NOT NULL, platform TEXT,
app_id TEXT, app_name TEXT, window_title TEXT, text TEXT, language TEXT,
char_count INTEGER NOT NULL, result_status TEXT, audio_rel_path TEXT
)`,
		`CREATE VIEW agent_voice_history_v1 AS SELECT
id, started_at_ms, ended_at_ms, timezone_offset_min, duration_ms, platform,
app_id, app_name, window_title, text, language, char_count, result_status,
audio_rel_path FROM voice_history`,
		`CREATE TABLE usage_daily (
local_date TEXT PRIMARY KEY, successful_count INTEGER NOT NULL,
duration_ms INTEGER NOT NULL, char_count INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL
)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insertCLIVoiceRow(t *testing.T, db *sql.DB, id int64, started time.Time, app, text string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO voice_history (
id, started_at_ms, ended_at_ms, timezone_offset_min, duration_ms, platform,
app_id, app_name, window_title, text, language, char_count, result_status, audio_rel_path
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, started.UnixMilli(), started.Add(5*time.Second).UnixMilli(), 480, 4321,
		"macos", "internal.app", app, nil, text, nil, 88, "success", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVoiceCLIUnfilteredListDefaultsToRecentThreeDays(t *testing.T) {
	home := sandbox(t)
	db := createCLIVoiceDatabase(t, home)
	fixedNow := time.Date(2026, 8, 4, 18, 0, 0, 0, time.Local)
	oldNow := voiceQueryNow
	voiceQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { voiceQueryNow = oldNow })
	base := time.Date(2026, 8, 2, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	insertCLIVoiceRow(t, db, 4, fixedNow.Add(-73*time.Hour), "ChatGPT", "三天以前的内容")
	insertCLIVoiceRow(t, db, 1, base, "ChatGPT", "第一个项目")
	insertCLIVoiceRow(t, db, 2, base.Add(24*time.Hour), "飞书", "讨论第二个项目")
	insertCLIVoiceRow(t, db, 3, base.Add(48*time.Hour), "飞书", "今天的内容")

	out, code := execCLI(t, "voice", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("voice list failed: %s", out)
	}
	result := decode(t, out)
	if result["total"] != float64(3) {
		t.Fatalf("unfiltered voice list must use the recent-three-day window: %s", out)
	}
	if result["default_range_applied"] != true || !strings.Contains(result["notice"].(string), "最近 3 天") {
		t.Fatalf("unfiltered voice list must explain its default range: %s", out)
	}
	rangeValue := result["range"].(map[string]any)
	if rangeValue["from"] != fixedNow.Add(-72*time.Hour).Format(time.RFC3339Nano) || rangeValue["to"] != fixedNow.Format(time.RFC3339Nano) {
		t.Fatalf("unfiltered voice list range = %+v", rangeValue)
	}
	items := result["items"].([]any)
	if items[0].(map[string]any)["id"] != float64(3) {
		t.Fatalf("voice list order is not newest first: %s", out)
	}
	if item := items[0].(map[string]any); item["duration_ms"] != float64(4321) || item["char_count"] != float64(88) {
		t.Fatalf("voice list changed stored metrics: %+v", item)
	}
	if _, exists := items[0].(map[string]any)["app_id"]; exists {
		t.Fatalf("voice list must not expose app_id: %s", out)
	}

	out, code = execCLI(t, "voice", "list", "--all", "--format", "json")
	allResult := decode(t, out)
	if code != 0 || allResult["total"] != float64(4) {
		t.Fatalf("voice list --all must bypass the default window: code=%d %s", code, out)
	}
	if _, exists := allResult["notice"]; exists {
		t.Fatalf("voice list --all must not claim a default window: %s", out)
	}
	out, code = execCLI(t, "voice", "list", "--from", "2026-08-01", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(4) {
		t.Fatalf("an explicit filter must bypass the default window: code=%d %s", code, out)
	}

	out, code = execCLI(t, "voice", "list", "--format", "ndjson")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 3 || !strings.Contains(lines[0], `"text":"今天的内容"`) {
		t.Fatalf("voice list NDJSON must stream complete rows: code=%d %q", code, out)
	}
	out, code = execCLI(t, "voice", "list", "--limit", "1", "--format", "table")
	if code != 0 || !strings.Contains(out, "app_name") || !strings.Contains(out, "今天的内容") {
		t.Fatalf("voice list table output failed: code=%d %q", code, out)
	}

	out, code = execCLI(t, "voice", "search", "第二个", "--app", "飞书", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("voice search/app filter failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "search", "三天以前", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("keyword search itself is a filter and must search all history: code=%d %s", code, out)
	}

	out, code = execCLI(t, "voice", "apps", "--format", "json")
	if code != 0 {
		t.Fatalf("voice apps failed: %s", out)
	}
	apps := decode(t, out)["apps"].([]any)
	if len(apps) != 2 || apps[0].(map[string]any)["app_name"] != "飞书" || apps[0].(map[string]any)["history_count"] != float64(2) {
		t.Fatalf("voice apps unexpected: %s", out)
	}
	out, code = execCLI(t, "voice", "apps", "--from", "2026-08-04", "--to", "2026-08-05", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("voice apps must respect the caller's time range: code=%d %s", code, out)
	}

	out, code = execCLI(t, "voice", "+latest", "--format", "json")
	if code != 0 || decode(t, out)["voice"].(map[string]any)["id"] != float64(3) {
		t.Fatalf("voice +latest failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "show", "2", "--format", "json")
	if code != 0 || decode(t, out)["voice"].(map[string]any)["text"] != "讨论第二个项目" {
		t.Fatalf("voice show failed: code=%d %s", code, out)
	}
}

func TestVoiceTodayAndStatsRespectExplicitRanges(t *testing.T) {
	home := sandbox(t)
	db := createCLIVoiceDatabase(t, home)
	fixedNow := time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)
	insertCLIVoiceRow(t, db, 1, fixedNow.Add(-48*time.Hour), "Example", "old")
	insertCLIVoiceRow(t, db, 2, fixedNow.Add(-time.Hour), "Example", "today")
	if _, err := db.Exec(`INSERT INTO usage_daily
(local_date, successful_count, duration_ms, char_count, updated_at_ms) VALUES
('2026-08-03', 12, 9000, 30, ?), ('2026-08-04', 2, 5000, 10, ?)`,
		fixedNow.Add(-24*time.Hour).UnixMilli(), fixedNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	oldNow := voiceQueryNow
	voiceQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { voiceQueryNow = oldNow })

	out, code := execCLI(t, "voice", "+today", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("voice +today failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "voice", "stats", "--from", "2026-08-04", "--to", "2026-08-05", "--format", "json")
	if code != 0 {
		t.Fatalf("voice stats failed: %s", out)
	}
	total := decode(t, out)["total"].(map[string]any)
	if total["successful_count"] != float64(2) || total["duration_ms"] != float64(5000) {
		t.Fatalf("voice stats did not use usage_daily: %s", out)
	}
}

func TestVoiceCLIValidationAndMissingStorage(t *testing.T) {
	home := sandbox(t)
	out, code := execCLI(t, "voice", "list", "--format", "json")
	if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_VOICE_STORAGE_NOT_FOUND" {
		t.Fatalf("missing voice storage should be explicit: code=%d %s", code, out)
	}

	out, code = execCLI(t, "voice", "storage-path", "--format", "json")
	if code != 0 || decode(t, out)["path"] != filepath.Join(home, "profiles", "default", "voice") {
		t.Fatalf("voice storage-path failed without DB: code=%d %s", code, out)
	}

	for _, args := range [][]string{
		{"voice", "list", "--limit", "0"},
		{"voice", "list", "--from", "2026-08-05", "--to", "2026-08-04"},
		{"voice", "search", "   "},
		{"voice", "show", "not-a-number"},
		{"voice", "stats", "--from", "2026-08-04T00:00:00Z"},
	} {
		out, code = execCLI(t, args...)
		if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
			t.Fatalf("%v should be invalid: code=%d %s", args, code, out)
		}
	}
}

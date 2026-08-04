package voice

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

func createVoiceTestDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "voice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, databaseFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
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
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
	// 把 schema checkpoint 到主库；后续测试数据只留在活动 WAL 中。
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	return dir, db
}

func insertVoiceHistory(t *testing.T, db *sql.DB, values ...any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO voice_history (
id, started_at_ms, ended_at_ms, timezone_offset_min, duration_ms, platform,
app_id, app_name, window_title, text, language, char_count, result_status, audio_rel_path
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryReadsActiveWALAndFiltersHistory(t *testing.T) {
	dir, writer := createVoiceTestDatabase(t)
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	insertVoiceHistory(t, writer,
		1, base.UnixMilli(), base.Add(20*time.Second).UnixMilli(), 480, 111,
		"macos", "com.example.chat", "ChatGPT", "Question", "Hello Project", nil,
		99, "success", "audio/inside.wav",
	)
	insertVoiceHistory(t, writer,
		2, base.Add(time.Minute).UnixMilli(), base.Add(time.Minute+10*time.Second).UnixMilli(), 480, 222,
		"macos", "com.example.lark", "飞书", nil, "讨论上线计划", "zh-CN",
		7, "success", nil,
	)
	insertVoiceHistory(t, writer,
		3, base.Add(2*time.Minute).UnixMilli(), base.Add(2*time.Minute+10*time.Second).UnixMilli(), 480, 333,
		"macos", "com.example.chat.beta", "chatgpt", nil, "HELLO again", "en",
		5, "failed", "../outside.wav",
	)
	if err := os.MkdirAll(filepath.Join(dir, "audio"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audio", "inside.wav"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.wav"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	items, err := repository.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].ID != 3 || items[2].ID != 1 {
		t.Fatalf("WAL rows or ordering incorrect: %+v", items)
	}
	if items[2].DurationMS != 111 || items[2].CharCount != 99 {
		t.Fatalf("stored metrics must remain authoritative: %+v", items[2])
	}
	if !items[2].HasAudio || items[2].AudioPath == nil {
		t.Fatalf("existing in-root audio was not resolved: %+v", items[2])
	}
	if items[0].HasAudio || items[0].AudioPath != nil {
		t.Fatalf("traversal audio path must be rejected: %+v", items[0])
	}

	search, err := repository.List(context.Background(), Query{Keyword: "hello"})
	if err != nil || len(search) != 2 {
		t.Fatalf("case-insensitive text search = %+v, err=%v", search, err)
	}
	byApp, err := repository.List(context.Background(), Query{App: " CHATGPT "})
	if err != nil || len(byApp) != 2 {
		t.Fatalf("normalized app matching = %+v, err=%v", byApp, err)
	}
	withAudio, err := repository.List(context.Background(), Query{HasAudio: true})
	if err != nil || len(withAudio) != 1 || withAudio[0].ID != 1 {
		t.Fatalf("real audio filter = %+v, err=%v", withAudio, err)
	}
	limited, err := repository.List(context.Background(), Query{Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].ID != 3 {
		t.Fatalf("explicit limit = %+v, err=%v", limited, err)
	}

	shown, err := repository.Show(context.Background(), 2)
	if err != nil || shown.ID != 2 || shown.Language == nil || *shown.Language != "zh-CN" {
		t.Fatalf("show = %+v, err=%v", shown, err)
	}
	_, err = repository.Show(context.Background(), 999)
	var typed *errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.CodeNotFound {
		t.Fatalf("missing show error = %v", err)
	}

	apps, err := repository.Apps(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].AppName != "chatgpt" || apps[0].HistoryCount != 2 {
		t.Fatalf("app dedupe/count/order = %+v", apps)
	}
	if _, err := repository.db.ExecContext(context.Background(), "DELETE FROM voice_history"); err == nil {
		t.Fatal("repository connection must reject writes")
	}
}

func TestRepositoryRangeAndUsageUseStoredFacts(t *testing.T) {
	dir, writer := createVoiceTestDatabase(t)
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	insertVoiceHistory(t, writer,
		1, base.UnixMilli(), base.Add(time.Second).UnixMilli(), 0, 500,
		"windows", "app", "Example", nil, "one", nil, 3, "success", nil,
	)
	insertVoiceHistory(t, writer,
		2, base.Add(24*time.Hour).UnixMilli(), base.Add(24*time.Hour+time.Second).UnixMilli(), 0, 600,
		"windows", "app", "Example", nil, "two", nil, 3, "success", nil,
	)
	_, err := writer.Exec(`INSERT INTO usage_daily
(local_date, successful_count, duration_ms, char_count, updated_at_ms) VALUES
('2026-08-03', 36, 305336, 853, ?), ('2026-08-04', 1, 5320, 2, ?)`,
		base.UnixMilli(), base.Add(24*time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	from := base.Add(24 * time.Hour)
	to := from.Add(24 * time.Hour)
	items, err := repository.List(context.Background(), Query{From: &from, To: &to})
	if err != nil || len(items) != 1 || items[0].ID != 2 {
		t.Fatalf("[from,to) history range = %+v, err=%v", items, err)
	}
	days, total, err := repository.Stats(context.Background(), "2026-08-04", "2026-08-05")
	if err != nil || len(days) != 1 {
		t.Fatalf("stats range = %+v, err=%v", days, err)
	}
	if total.SuccessfulCount != 1 || total.DurationMS != 5320 || total.CharCount != 2 {
		t.Fatalf("stats must use usage_daily facts: %+v", total)
	}
}

func TestRepositoryMissingStorageAndSchema(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "voice")
	_, err := Open(context.Background(), missing)
	var typed *errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.CodeVoiceStorageNotFound {
		t.Fatalf("missing storage error = %v", err)
	}

	dir := filepath.Join(t.TempDir(), "voice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, databaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	_, err = Open(context.Background(), dir)
	if !errors.As(err, &typed) || typed.Code != errs.CodeVoiceSchemaUnsupported {
		t.Fatalf("unsupported schema error = %v", err)
	}
}

func TestRepositoryEmptyDatabaseReturnsEmptyResults(t *testing.T) {
	dir, _ := createVoiceTestDatabase(t)
	repository, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	items, err := repository.List(context.Background(), Query{})
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty history = %#v, err=%v", items, err)
	}
	apps, err := repository.Apps(context.Background(), Query{})
	if err != nil || apps == nil || len(apps) != 0 {
		t.Fatalf("empty apps = %#v, err=%v", apps, err)
	}
	days, total, err := repository.Stats(context.Background(), "", "")
	if err != nil || days == nil || len(days) != 0 || total != (UsageTotal{}) {
		t.Fatalf("empty stats = %#v total=%+v, err=%v", days, total, err)
	}
}

func TestParseBoundaryAndLocalDate(t *testing.T) {
	date, err := ParseBoundary("2026-08-04", "--from")
	if err != nil || date.Hour() != 0 || date.Location() != time.Local {
		t.Fatalf("local date boundary = %v, err=%v", date, err)
	}
	instant, err := ParseBoundary("2026-08-04T10:30:00+08:00", "--to")
	if err != nil || instant.Format(time.RFC3339) != "2026-08-04T10:30:00+08:00" {
		t.Fatalf("RFC3339 boundary = %v, err=%v", instant, err)
	}
	if _, err := ParseBoundary("2026/08/04", "--from"); err == nil {
		t.Fatal("invalid history boundary should fail")
	}
	if _, err := ParseLocalDate("2026-08-04T00:00:00Z", "--from"); err == nil {
		t.Fatal("stats boundary must be a local date")
	}
}

func TestReadOnlyDatabaseURIHandlesWindowsDriveAndSpaces(t *testing.T) {
	got := readOnlyDatabaseURI(`C:\Users\Y J H\.yoooclaw\profiles\default\voice\voice.sqlite3`, "windows")
	if want := "file:///C:/Users/Y%20J%20H/.yoooclaw/profiles/default/voice/voice.sqlite3"; !strings.HasPrefix(got, want) {
		t.Fatalf("Windows SQLite URI = %q, want prefix %q", got, want)
	}
	if !strings.Contains(got, "mode=ro") || !strings.Contains(got, "query_only") || !strings.Contains(got, "busy_timeout") {
		t.Fatalf("read-only SQLite URI is missing safeguards: %q", got)
	}
}

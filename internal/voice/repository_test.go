package voice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

func voiceSource(id, startedAt, appID, appName, text string) map[string]any {
	started, _ := time.Parse(time.RFC3339Nano, startedAt)
	return map[string]any{
		"id": 42, "voice_id": id,
		"started_at": startedAt, "ended_at": started.Add(time.Second).Format(time.RFC3339Nano),
		"timezone_offset_min": 480, "duration_ms": 111, "platform": "macos",
		"app_id": appID, "app_name": appName, "window_title": nil,
		"text": text, "language": "zh-CN", "char_count": 7,
		"result_status": "success", "audio_rel_path": nil,
	}
}

func createVoiceJSONLRoot(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "voice")
	if err := os.MkdirAll(filepath.Join(dir, historyDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeVoiceDay(t *testing.T, root, date string, rows []map[string]any) {
	t.Helper()
	data := make([]byte, 0)
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(filepath.Join(root, historyDirName, date+".jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryReadsDailyJSONLAndFiltersHistory(t *testing.T) {
	root := createVoiceJSONLRoot(t)
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	one := voiceSource("voice-one", base.Format(time.RFC3339), "com.openai.codex", "ChatGPT", "Hello Project")
	one["audio_rel_path"] = "audio/2026/08/voice-one.wav"
	two := voiceSource("voice-two", base.Add(time.Minute).Format(time.RFC3339), "com.google.Chrome", "Google Chrome", "浏览器里的项目")
	three := voiceSource("voice-three", base.Add(2*time.Minute).Format(time.RFC3339), "com.microsoft.edgemac", "Microsoft Edge", "HELLO again")
	three["result_status"] = "failed"
	three["audio_rel_path"] = "../outside.wav"
	// 故意乱序，reader 必须在单个日期内自行排序。
	writeVoiceDay(t, root, "2026-08-04", []map[string]any{two, one, three})
	if err := os.MkdirAll(filepath.Join(root, "audio", "2026", "08"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "audio", "2026", "08", "voice-one.wav"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "outside.wav"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := repository.List(context.Background(), Query{})
	if err != nil || len(items) != 3 || items[0].ID != "voice-three" || items[2].ID != "voice-one" {
		t.Fatalf("history/order = %+v, err=%v", items, err)
	}
	if items[2].DurationMS != 111 || items[2].CharCount != 7 || !items[2].HasAudio || items[2].AudioPath == nil {
		t.Fatalf("stored metrics/audio facts changed: %+v", items[2])
	}
	if items[0].HasAudio || items[0].AudioPath != nil {
		t.Fatalf("traversal audio path must be rejected: %+v", items[0])
	}

	search, err := repository.List(context.Background(), Query{Keyword: "hello"})
	if err != nil || len(search) != 2 {
		t.Fatalf("Unicode case-insensitive text search = %+v, err=%v", search, err)
	}
	byAppID, err := repository.List(context.Background(), Query{App: " com.openai.codex "})
	if err != nil || len(byAppID) != 1 || byAppID[0].ID != "voice-one" {
		t.Fatalf("exact app_id filter = %+v, err=%v", byAppID, err)
	}
	byAppName, err := repository.List(context.Background(), Query{App: " chatgpt "})
	if err != nil || len(byAppName) != 1 || byAppName[0].ID != "voice-one" {
		t.Fatalf("exact app_name filter = %+v, err=%v", byAppName, err)
	}
	aliases, err := repository.List(context.Background(), Query{App: "GPT"})
	if err != nil || len(aliases) != 0 {
		t.Fatalf("aliases must not be expanded = %+v, err=%v", aliases, err)
	}
	withAudio, err := repository.List(context.Background(), Query{HasAudio: true, Limit: 1})
	if err != nil || len(withAudio) != 1 || withAudio[0].ID != "voice-one" {
		t.Fatalf("real audio filter/limit = %+v, err=%v", withAudio, err)
	}

	shown, err := repository.Show(context.Background(), "voice-two")
	if err != nil || shown.ID != "voice-two" || shown.Language == nil || *shown.Language != "zh-CN" {
		t.Fatalf("show by voice_id = %+v, err=%v", shown, err)
	}
	_, err = repository.Show(context.Background(), "999")
	var typed *errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.CodeNotFound {
		t.Fatalf("missing show error = %v", err)
	}

	apps, err := repository.Apps(context.Background(), Query{})
	if err != nil || len(apps) != 3 {
		t.Fatalf("apps = %+v, err=%v", apps, err)
	}
	counts := map[string]int64{}
	for _, app := range apps {
		counts[app.AppID+"|"+app.AppName] = app.HistoryCount
	}
	if counts["com.openai.codex|ChatGPT"] != 1 ||
		counts["com.google.Chrome|Google Chrome"] != 1 ||
		counts["com.microsoft.edgemac|Microsoft Edge"] != 1 {
		t.Fatalf("apps must return raw ID/name identities: %+v", apps)
	}
}

func TestAppsDeduplicatesByAppIDAndKeepsRawName(t *testing.T) {
	root := createVoiceJSONLRoot(t)
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	writeVoiceDay(t, root, "2026-08-04", []map[string]any{
		voiceSource("vscode-1", base.Format(time.RFC3339), "com.microsoft.VSCode", "Code", "one"),
		voiceSource("vscode-2", base.Add(time.Minute).Format(time.RFC3339), "com.microsoft.VSCode", "Code", "two"),
		voiceSource("other-code", base.Add(2*time.Minute).Format(time.RFC3339), "org.example.code", "Code", "three"),
	})
	repository, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := repository.Apps(context.Background(), Query{})
	if err != nil || len(apps) != 2 {
		t.Fatalf("apps = %+v, err=%v", apps, err)
	}
	byID := map[string]AppSummary{}
	for _, app := range apps {
		byID[app.AppID] = app
	}
	if got := byID["com.microsoft.VSCode"]; got.AppName != "Code" || got.HistoryCount != 2 {
		t.Fatalf("VS Code mapping/count = %+v", got)
	}
	items, err := repository.List(context.Background(), Query{App: "com.microsoft.VSCode"})
	if err != nil || len(items) != 2 {
		t.Fatalf("stable app_id filter = %+v, err=%v", items, err)
	}
	items, err = repository.List(context.Background(), Query{App: "VS Code"})
	if err != nil || len(items) != 0 {
		t.Fatalf("unobserved display name must not be treated as an alias: %+v, err=%v", items, err)
	}
}

func TestRepositoryRangeMalformedRowsAndIncompleteTail(t *testing.T) {
	root := createVoiceJSONLRoot(t)
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	writeVoiceDay(t, root, "2026-08-03", []map[string]any{
		voiceSource("old", base.Format(time.RFC3339), "unknown.id", "Unknown", "old"),
	})
	valid := voiceSource("new", base.Add(24*time.Hour).Format(time.RFC3339), "com.electron.lark", "飞书", "new")
	validLine, _ := json.Marshal(valid)
	partial := voiceSource("partial", base.Add(25*time.Hour).Format(time.RFC3339), "id", "Partial", "partial")
	partialLine, _ := json.Marshal(partial)
	raw := append(append(append(validLine, '\n'), []byte("{bad json}\n")...), partialLine...)
	if err := os.WriteFile(filepath.Join(root, historyDirName, "2026-08-04.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, historyDirName, "ignored.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	from := base.Add(24 * time.Hour)
	to := from.Add(24 * time.Hour)
	items, err := repository.List(context.Background(), Query{From: &from, To: &to})
	if err != nil || len(items) != 1 || items[0].ID != "new" {
		t.Fatalf("range/bad row/incomplete tail = %+v, err=%v", items, err)
	}
}

func TestRepositoryMissingJSONLDoesNotFallbackToSQLite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "voice")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "voice.sqlite3"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), root)
	var typed *errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.CodeVoiceStorageNotFound {
		t.Fatalf("missing JSONL storage must not fall back to SQLite: %v", err)
	}

	emptyRoot := createVoiceJSONLRoot(t)
	repository, err := Open(context.Background(), emptyRoot)
	if err != nil {
		t.Fatal(err)
	}
	items, err := repository.List(context.Background(), Query{})
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty history = %#v, err=%v", items, err)
	}
	apps, err := repository.Apps(context.Background(), Query{})
	if err != nil || apps == nil || len(apps) != 0 {
		t.Fatalf("empty apps = %#v, err=%v", apps, err)
	}
}

func TestParseBoundary(t *testing.T) {
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
}

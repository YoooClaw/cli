package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// assertOKJSON 断言退出码 0 且输出是合法 JSON（数组或对象）。
func assertOKJSON(t *testing.T, label, out string, code int) {
	t.Helper()
	if code != 0 {
		t.Errorf("%s: expected exit 0, got %d (%s)", label, code, out)
		return
	}
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Errorf("%s: output not valid JSON: %q", label, out)
	}
}

func TestLocalReadCommandsOnEmptyStore(t *testing.T) {
	sandbox(t)
	cases := [][]string{
		{"recording", "list"},
		{"recording", "storage-path"},
		{"image", "list"},
		{"image", "storage-path"},
		{"synced-web-page", "list"},
		{"synced-web-page", "storage-path"},
		{"notification", "storage-path"},
		{"notification", "search"},
		{"notification", "stats"},
		{"notification", "summary"},
		{"notification", "+today"},
		{"notification", "+recent"},
		{"sync", "scan"},
		{"log"},
		{"log", "+errors"},
		{"profile", "list"},
	}
	for _, args := range cases {
		out, code := execCLI(t, args...)
		assertOKJSON(t, strings.Join(args, " "), out, code)
	}
}

func TestProfileCreateUseDelete(t *testing.T) {
	home := sandbox(t)
	imp := home + "/import.json"
	if err := os.WriteFile(imp, []byte(`{"daemon":{"port":20003}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := execCLI(t, "profile", "create", "work", "--non-interactive", "--from-file", imp, "--no-start"); code != 0 {
		t.Fatalf("profile create: %s", out)
	}
	out, code := execCLI(t, "profile", "list")
	if code != 0 || !strings.Contains(out, "work") {
		t.Errorf("profile list should include work: %s", out)
	}
	// 再建一个待删除的 profile
	if out, code := execCLI(t, "profile", "create", "spare", "--non-interactive", "--from-file", imp, "--no-start"); code != 0 {
		t.Fatalf("profile create spare: %s", out)
	}
	if out, code := execCLI(t, "profile", "use", "work"); code != 0 {
		t.Errorf("profile use failed: %s", out)
	}
	// 删除非 active profile（active=work，删 spare）
	if out, code := execCLI(t, "profile", "delete", "spare", "--yes"); code != 0 {
		t.Errorf("profile delete spare: %s", out)
	}
	// use 不存在的 profile -> PROFILE_NOT_FOUND
	if _, code := execCLI(t, "profile", "use", "ghost"); code == 0 {
		t.Error("use missing profile should fail")
	}
}

func TestProfileUseTransfersRunningManagedService(t *testing.T) {
	home := sandbox(t)
	imp := filepath.Join(home, "import.json")
	if err := os.WriteFile(imp, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := execCLI(t, "config", "init", "--non-interactive", "--from-file", imp, "--no-start"); code != 0 {
		t.Fatalf("default init: %s", out)
	}
	if out, code := execCLI(t, "profile", "create", "work", "--non-interactive", "--from-file", imp, "--no-start"); code != 0 {
		t.Fatalf("profile create: %s", out)
	}
	if out, code := execCLI(t, "daemon", "autostart", "enable"); code != 0 {
		t.Fatalf("autostart enable: %s", out)
	}
	out, code := execCLI(t, "profile", "use", "work")
	if code != 0 {
		t.Fatalf("profile use: %s", out)
	}
	result := decode(t, out)
	daemonInfo := result["daemon"].(map[string]any)
	if daemonInfo["started"] != true || daemonInfo["supervised"] != true {
		t.Fatalf("managed service was not transferred: %+v", result)
	}
	out, code = execCLI(t, "daemon", "autostart", "status")
	status := decode(t, out)
	if code != 0 || status["profile"] != "work" || status["running"] != true {
		t.Fatalf("unexpected service status after profile use: %s", out)
	}
}

func TestProfileUseRepairsStaleActiveProfile(t *testing.T) {
	home := sandbox(t)
	if err := os.MkdirAll(filepath.Join(home, "profiles", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "active-profile"), []byte("deleted-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := execCLI(t, "profile", "use", "default", "--format", "json")
	if code != 0 {
		t.Fatalf("profile use default failed: %s", out)
	}
	raw, err := os.ReadFile(paths.ActiveProfilePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != paths.DefaultProfile {
		t.Fatalf("stale active-profile was not repaired: %q", raw)
	}
}

func TestNotificationSearchInvalidArg(t *testing.T) {
	sandbox(t)
	// 非法 --conversation-type
	out, code := execCLI(t, "notification", "search", "--conversation-type", "channel")
	if code == 0 {
		t.Error("invalid conversation-type should fail")
	}
	if !strings.Contains(out, "YOOOCLAW_INVALID_ARGUMENT") {
		t.Errorf("expected INVALID_ARGUMENT: %s", out)
	}
}

func TestRecordingStatusNotFound(t *testing.T) {
	sandbox(t)
	out, code := execCLI(t, "recording", "status", "ghost-id")
	if code == 0 {
		t.Error("status of missing recording should fail")
	}
	if !strings.Contains(out, "error") {
		t.Errorf("expected error payload: %s", out)
	}
}

func TestRecordingListAndLatestSortByActualTime(t *testing.T) {
	home := sandbox(t)
	recordingsDir := filepath.Join(home, "profiles", "default", "recordings")
	if err := os.MkdirAll(recordingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	index := `{"recordings":[
		{"id":"earlier-local","metadata":{"name":"earlier","created_at":"2026-07-29T11:02:00+08:00"},"status":"transcribed"},
		{"id":"later-utc","metadata":{"name":"later","duration_sec":16,"created_at":"2026-07-29T05:47:00Z","file_size_display":"5.9 MB"},"status":"transcribed"}
	]}`
	if err := os.WriteFile(filepath.Join(recordingsDir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := execCLI(t, "recording", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("recording list failed: %s", out)
	}
	listResult := decode(t, out)
	items, ok := listResult["recordings"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("unexpected recording list: %+v", listResult)
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "later-utc" {
		t.Fatalf("recording list did not put actual latest first: %+v", items)
	}
	if first["file_size_display"] != "5.9 MB" {
		t.Fatalf("recording list file_size_display = %v", first["file_size_display"])
	}
	if first["duration_display"] != "16s" {
		t.Fatalf("recording list duration_display = %v", first["duration_display"])
	}
	if _, exists := first["file_size_bytes"]; exists {
		t.Fatalf("recording list must not expose removed file_size_bytes: %+v", first)
	}

	out, code = execCLI(t, "recording", "+latest", "--format", "json")
	if code != 0 {
		t.Fatalf("recording +latest failed: %s", out)
	}
	latestResult := decode(t, out)
	latest, _ := latestResult["recording"].(map[string]any)
	if latest["id"] != "later-utc" {
		t.Fatalf("recording +latest returned wrong item: %+v", latestResult)
	}
	if latest["file_size_display"] != "5.9 MB" {
		t.Fatalf("recording +latest file_size_display = %v", latest["file_size_display"])
	}
	if latest["duration_display"] != "16s" {
		t.Fatalf("recording +latest duration_display = %v", latest["duration_display"])
	}
	if _, exists := latest["file_size_bytes"]; exists {
		t.Fatalf("recording +latest must not expose removed file_size_bytes: %+v", latest)
	}
}

func TestRecordingTodayUsesLocalCalendarDayWithoutLatestFallback(t *testing.T) {
	home := sandbox(t)
	recordingsDir := filepath.Join(home, "profiles", "default", "recordings")
	if err := os.MkdirAll(recordingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldNow := recordingQueryNow
	fixedNow := time.Date(2026, 7, 29, 10, 30, 0, 0, time.Local)
	recordingQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { recordingQueryNow = oldNow })
	now := recordingQueryNow().In(time.Local)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	yesterday := todayStart.Add(-time.Minute)
	todayLocal := todayStart.Add(2 * time.Hour)
	todayAsUTC := todayStart.Add(3 * time.Hour).UTC()
	tomorrow := tomorrowStart.Add(time.Minute)
	index := fmt.Sprintf(`{"recordings":[
		{"id":"yesterday","metadata":{"name":"yesterday","created_at":%q},"status":"transcribed"},
		{"id":"today-local","metadata":{"name":"today-local","created_at":%q},"status":"transcribed"},
		{"id":"today-utc","metadata":{"name":"today-utc","created_at":%q},"status":"transcribed"},
		{"id":"tomorrow","metadata":{"name":"tomorrow","created_at":%q},"status":"transcribed"}
	]}`,
		yesterday.Format(time.RFC3339), todayLocal.Format(time.RFC3339),
		todayAsUTC.Format(time.RFC3339), tomorrow.Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(recordingsDir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := execCLI(t, "recording", "+today", "--format", "json")
	if code != 0 {
		t.Fatalf("recording +today failed: %s", out)
	}
	result := decode(t, out)
	items, ok := result["recordings"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("+today should return only today's recordings: %+v", result)
	}
	if result["date"] != todayStart.Format("2006-01-02") {
		t.Fatalf("+today date = %v", result["date"])
	}
	for _, item := range items {
		id := item.(map[string]any)["id"]
		if id == "yesterday" || id == "tomorrow" {
			t.Fatalf("+today leaked an out-of-range recording: %+v", items)
		}
	}

	out, code = execCLI(t, "recording", "list",
		"--from", todayStart.Format(time.RFC3339),
		"--to", tomorrowStart.Format(time.RFC3339),
		"--format", "json")
	if code != 0 {
		t.Fatalf("recording list range failed: %s", out)
	}
	if got := decode(t, out)["total"]; got != float64(2) {
		t.Fatalf("recording list range total = %v, want 2", got)
	}
}

func TestRecordingTodayReturnsEmptyInsteadOfYesterday(t *testing.T) {
	home := sandbox(t)
	recordingsDir := filepath.Join(home, "profiles", "default", "recordings")
	if err := os.MkdirAll(recordingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldNow := recordingQueryNow
	fixedNow := time.Date(2026, 7, 29, 10, 30, 0, 0, time.Local)
	recordingQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { recordingQueryNow = oldNow })
	now := recordingQueryNow().In(time.Local)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	index := fmt.Sprintf(`{"recordings":[
		{"id":"yesterday","metadata":{"name":"yesterday","created_at":%q},"status":"transcribed"}
	]}`, todayStart.Add(-time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(recordingsDir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := execCLI(t, "recording", "+today", "--format", "json")
	if code != 0 {
		t.Fatalf("recording +today failed: %s", out)
	}
	result := decode(t, out)
	if result["total"] != float64(0) {
		t.Fatalf("+today must not fall back to yesterday: %+v", result)
	}
	items, ok := result["recordings"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("+today recordings should be an empty array: %+v", result)
	}
}

func TestRecordingListRejectsInvalidTimeRange(t *testing.T) {
	sandbox(t)
	out, code := execCLI(t, "recording", "list", "--from", "not-a-time", "--format", "json")
	if code == 0 || !strings.Contains(out, errs.CodeInvalidArgument) {
		t.Fatalf("invalid recording range should fail: code=%d out=%s", code, out)
	}
}

func TestRecordingCommandsRenderTableFromResultEnvelope(t *testing.T) {
	home := sandbox(t)
	recordingsDir := filepath.Join(home, "profiles", "default", "recordings")
	if err := os.MkdirAll(recordingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldNow := recordingQueryNow
	fixedNow := time.Date(2026, 7, 31, 12, 0, 0, 0, time.Local)
	recordingQueryNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { recordingQueryNow = oldNow })
	createdAt := fixedNow.Add(-time.Hour).Format(time.RFC3339)
	index := fmt.Sprintf(`{"recordings":[
		{"id":"table-recording","metadata":{"name":"table test","created_at":%q},"status":"transcribed"}
	]}`, createdAt)
	if err := os.WriteFile(filepath.Join(recordingsDir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := [][]string{
		{"recording", "list", "--format", "table"},
		{"recording", "+today", "--format", "table"},
		{"recording", "+latest", "--format", "table"},
	}
	for _, args := range cases {
		out, code := execCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v failed: %s", args, out)
		}
		if json.Valid([]byte(strings.TrimSpace(out))) {
			t.Fatalf("%v returned JSON instead of a table: %s", args, out)
		}
		for _, want := range []string{"id", "name", "table-recording", "table test"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v table missing %q: %s", args, want, out)
			}
		}
	}
}

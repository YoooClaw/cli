package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{"id":"later-utc","metadata":{"name":"later","created_at":"2026-07-29T05:47:00Z"},"status":"transcribed"}
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

	out, code = execCLI(t, "recording", "+latest", "--format", "json")
	if code != 0 {
		t.Fatalf("recording +latest failed: %s", out)
	}
	latestResult := decode(t, out)
	latest, _ := latestResult["recording"].(map[string]any)
	if latest["id"] != "later-utc" {
		t.Fatalf("recording +latest returned wrong item: %+v", latestResult)
	}
}

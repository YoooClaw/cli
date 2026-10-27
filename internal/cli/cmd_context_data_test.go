package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextCommandsCalendarDefaultsAndMemoryRoundTrip(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, "profiles", "default", "calendars")
	os.MkdirAll(dir, 0700)
	raw := `{"startAt":"2026-10-22T13:00:00+08:00","endAt":"2026-10-22T14:00:00+08:00","timeZone":"Asia/Shanghai","title":"example"}` + "\n"
	os.WriteFile(filepath.Join(dir, "2026-10-22.jsonl"), []byte(raw), 0600)
	old := contextQueryNow
	contextQueryNow = func() time.Time { v, _ := time.Parse(time.RFC3339, "2026-10-22T12:00:00+08:00"); return v }
	t.Cleanup(func() { contextQueryNow = old })
	call := func(args ...string) map[string]any {
		t.Helper()
		out, code := execCLI(t, append(args, "--format", "json")...)
		if code != 0 {
			t.Fatal(out)
		}
		return decode(t, out)
	}
	a := call("calendar", "list")
	if a["scope"] != "upcoming" || a["scopeDescription"] == "" {
		t.Fatal(a)
	}
	a = call("calendar", "list", "--all")
	if a["scope"] != "all_synced" {
		t.Fatal(a)
	}
	a = call("sync", "next", "--source", "calendars", "--all")
	token := a["token"].(string)
	a = call("sync", "commit", "--source", "calendars", "--token", token)
	if a["commitMode"] != "exact-snapshot" {
		t.Fatal(a)
	}
	a = call("sync", "next", "--source", "calendars", "--all")
	if a["done"] != true {
		t.Fatal(a)
	}
}

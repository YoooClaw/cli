package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestLastDaysUsesElapsedHoursAndHalfOpenBounds(t *testing.T) {
	home := sandbox(t)
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 11, 4, 12, 34, 56, 0, zone)
	start := end.Add(-168 * time.Hour) // crosses the autumn clock change
	oldNow := contextQueryNow
	contextQueryNow = func() time.Time { return end }
	t.Cleanup(func() { contextQueryNow = oldNow })

	dir := paths.ForRoot(home, "default").Notifications
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	byDay := map[string][]notif.StoredNotification{}
	for _, at := range []time.Time{start.Add(-time.Second), start, end.Add(-time.Second), end} {
		day := at.In(time.Local).Format(time.DateOnly)
		byDay[day] = append(byDay[day], notif.StoredNotification{Timestamp: at.Format(time.RFC3339)})
	}
	for day, items := range byDay {
		data, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, day+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"summary", "stats"} {
		out, code := execCLI(t, "notification", action, "--last-days", "7")
		reply := decode(t, out)
		if code != 0 || reply["total"] != 2.0 {
			t.Fatal(out)
		}
		bounds := reply["range"].(map[string]any)
		for key, want := range map[string]time.Time{"from": start, "to": end} {
			got, err := time.Parse(time.RFC3339Nano, bounds[key].(string))
			if err != nil || !got.Equal(want) {
				t.Fatalf("%s: got %v want %v", key, bounds[key], want)
			}
		}
	}
	// Explicit notification ranges keep their historical inclusive upper bound.
	out, code := execCLI(t, "notification", "summary", "--from", start.In(time.Local).Format(time.RFC3339), "--to", end.In(time.Local).Format(time.RFC3339))
	if code != 0 || decode(t, out)["total"] != 3.0 {
		t.Fatal(out)
	}
}

func TestLastDaysRejectsInvalidAndConflictingFlags(t *testing.T) {
	sandbox(t)
	for _, args := range [][]string{
		{"recording", "list", "--last-days", "0"}, {"image", "list", "--last-days", "-1"},
		{"notification", "summary-job", "create", "--last-days", "999999999"},
		{"voice", "apps", "--last-days", "1.5"},
		{"synced-web-page", "search", "word", "--last-days", "7", "--to", "2026-11-27"},
		{"voice", "search", "word", "--last-days", "7", "--from", "2026-11-20"},
		{"voice", "list", "--last-days", "7", "--all"}, {"calendar", "list", "--last-days", "7", "--date", "2026-11-20"},
	} {
		out, code := execCLI(t, args...)
		if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
			t.Fatalf("%v: code=%d output=%s", args, code, out)
		}
	}
}

func TestImageAndCalendarListsUseRollingBoundaries(t *testing.T) {
	dirs := paths.ForRoot(sandbox(t), "default")
	oldNow := contextQueryNow
	contextQueryNow = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { contextQueryNow = oldNow })
	for _, dir := range []string{dirs.Images, dirs.Calendars} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	images := `{"images":[
		{"imageId":"before","metadata":{"created_at":"2026-11-20T19:59:59+08:00"}},
		{"imageId":"start","metadata":{"created_at":"2026-11-20T20:00:00+08:00"}},
		{"imageId":"end","metadata":{"created_at":"2026-11-27T20:00:00+08:00"}}]}`
	if err := os.WriteFile(filepath.Join(dirs.Images, "index.json"), []byte(images), 0600); err != nil {
		t.Fatal(err)
	}
	out, code := execCLI(t, "image", "list", "--last-days", "7")
	rows := decode(t, out)["images"].([]any)
	if code != 0 || len(rows) != 1 || rows[0].(map[string]any)["imageId"] != "start" {
		t.Fatal(out)
	}

	events := `{"title":"ended","startAt":"2026-11-20T10:00:00Z","endAt":"2026-11-20T12:00:00Z","timeZone":"UTC"}
{"title":"overlap","startAt":"2026-11-20T11:00:00Z","endAt":"2026-11-20T13:00:00Z","timeZone":"UTC"}
`
	if err := os.WriteFile(filepath.Join(dirs.Calendars, "2026-11-20.jsonl"), []byte(events), 0600); err != nil {
		t.Fatal(err)
	}
	atEnd := `{"title":"future","startAt":"2026-11-27T12:00:00Z","endAt":"2026-11-27T13:00:00Z","timeZone":"UTC"}`
	if err := os.WriteFile(filepath.Join(dirs.Calendars, "2026-11-27.jsonl"), []byte(atEnd), 0600); err != nil {
		t.Fatal(err)
	}
	out, code = execCLI(t, "calendar", "list", "--last-days", "7")
	result := decode(t, out)
	rows = result["items"].([]any)
	if code != 0 || result["scope"] != "rolling_window" || len(rows) != 1 || rows[0].(map[string]any)["data"].(map[string]any)["title"] != "overlap" {
		t.Fatal(out)
	}
}

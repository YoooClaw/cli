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

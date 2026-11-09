package contextdata

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestHealthMemoryReturnsCurrentSnapshotOnly(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	sleepFixture(t, p, "core", "2026-10-22T01:00:00+08:00")
	first, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := first["items"].([]Change)[0].Version
	if _, err = Commit(p, "health-data", first["token"].(string)); err != nil {
		t.Fatal(err)
	}
	path, _ := statePath(p, "health-data")
	state, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Seen["2026-10-22"].Data != nil {
		t.Fatal("health checkpoint retains old data")
	}
	sleepFixture(t, p, "deep", "2026-10-22T02:00:00+08:00")
	next, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	change := next["items"].([]Change)[0]
	if change.PreviousVersion != oldVersion || change.Version == oldVersion || change.PreviousData != nil {
		t.Fatal(change)
	}
	// An in-flight batch created before this change must also omit the old data.
	state, err = loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	state.Pending.Items[0].PreviousData = map[string]any{"moodCount": 1}
	if err = fsutil.WriteJSON(path, state, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	replay, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(replay)
	after, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"previousData"`) || hasSection(t, replay, "moods") || replay["token"] != next["token"] || string(before) != string(after) {
		t.Fatal("pending projection changed evidence or leaked old data")
	}
	if _, err = Commit(p, "health-data", replay["token"].(string)); err != nil {
		t.Fatal(err)
	}
	done, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil || done["done"] != true {
		t.Fatal(done, err)
	}
}

func TestOtherSnapshotSourcesKeepPreviousData(t *testing.T) {
	for _, source := range []string{"calendars", "reminders"} {
		p := paths.ForRoot(t.TempDir(), "default")
		current := Item{ID: "one", Date: "1997-07-16", Version: "v1", Data: map[string]any{"id": "one", "title": "before"}}
		read := func() (Dataset, error) { return Dataset{Connected: true, Items: []Item{current}}, nil }
		first, err := NextWithReader(p, source, Options{All: true}, false, read)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Commit(p, source, first["token"].(string)); err != nil {
			t.Fatal(err)
		}
		current.Version = "v2"
		current.Data = map[string]any{"id": "one", "title": "after"}
		next, err := NextWithReader(p, source, Options{All: true}, false, read)
		if err != nil {
			t.Fatal(err)
		}
		if next["items"].([]Change)[0].PreviousData["title"] != "before" {
			t.Fatal(source, next)
		}
	}
}

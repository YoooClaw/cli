package contextdata

import (
	"encoding/json"
	"github.com/YoooClaw/cli/internal/healthdata"
	"github.com/YoooClaw/cli/internal/paths"
	"os"
	"strings"
	"testing"
	"time"
)

func hasSection(t *testing.T, r map[string]any, kind string) bool {
	t.Helper()
	s, ok := r["sourceSemantics"].(healthdata.FieldSemantics)
	if !ok {
		t.Fatal("missing sourceSemantics", r)
	}
	for _, section := range s.Sections {
		if section.Type == kind {
			return true
		}
	}
	return false
}
func TestHealthSemanticsAreResponseMetadata(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	sleepFixture(t, p, "core", "2026-10-22T01:00:00+08:00")
	before, err := Read(p, "health-data")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"list", "get", "series"} {
		r, err := Query(p, "health-data", mode, "", Options{All: true, Limit: 100, Metric: "steps"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if hasSection(t, r, "sleepSessions") != (mode != "series") {
			t.Fatal(mode, r)
		}
	}
	batch, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil || !hasSection(t, batch, "sleepSessions") {
		t.Fatal(batch, err)
	}
	again, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil || again["token"] != batch["token"] {
		t.Fatal(again, err)
	}
	path, _ := statePath(p, "health-data")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "sourceSemantics") {
		t.Fatal("metadata persisted in batch")
	}
	_, err = Commit(p, "health-data", batch["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	done, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil || done["done"] != true || hasSection(t, done, "sleepSessions") {
		t.Fatal(done, err)
	}
	after, _ := Read(p, "health-data")
	if before.Items[0].Version != after.Items[0].Version {
		t.Fatal("version changed")
	}
	previous := map[string]any{"moodCount": 1}
	item := Change{Item: Item{Data: map[string]any{"items": []map[string]any{{"metric": "steps", "value": 1}}}}, PreviousData: previous}
	r := batchResult("health-data", &Batch{Items: []Change{item}})
	if hasSection(t, r, "moods") {
		t.Fatal("previous-only type leaked")
	}
	data, _ := json.Marshal(item)
	if strings.Contains(string(data), "sourceSemantics") {
		t.Fatal("metadata leaked into evidence")
	}
	if _, ok := batchResult("calendars", &Batch{})["sourceSemantics"]; ok {
		t.Fatal("unrelated source changed")
	}
}

func TestHealthSemanticsSelectOnlyReturnedPage(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	sleepFixture(t, p, "core", "2026-10-22T01:00:00+08:00")
	fixture(t, p.HealthData+"/2026-10-21.jsonl",
		map[string]any{"schemaVersion": 3, "date": "2026-10-21", "timeZone": "Asia/Shanghai", "provider": "test", "updatedAt": "2026-10-21T12:00:00+08:00"},
		map[string]any{"metric": "steps", "name": "Steps", "unit": "count", "value": 3000})
	o := Options{All: true, Limit: 1}
	types := map[string]bool{}
	for {
		r, err := Query(p, "health-data", "list", "", o, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		sleep := hasSection(t, r, "sleepSessions")
		metrics := hasSection(t, r, "items")
		if sleep == metrics {
			t.Fatal("page explanations include unrelated type", r)
		}
		if sleep {
			types["sleep"] = true
		}
		if metrics {
			types["metrics"] = true
		}
		next, ok := r["nextCursor"].(string)
		if !ok {
			break
		}
		o.Cursor = next
	}
	if len(types) != 2 {
		t.Fatal(types)
	}
}

func TestSemanticsIsolationAndScanDoNotChangeCursorOrPending(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	sleepFixture(t, p, "core", "2026-10-22T01:00:00+08:00")
	fixture(t, p.HealthData+"/2026-10-21.jsonl",
		map[string]any{"schemaVersion": 3, "date": "2026-10-21", "timeZone": "Asia/Shanghai", "provider": "test", "updatedAt": "2026-10-21T12:00:00+08:00"},
		map[string]any{"metric": "steps", "name": "Steps", "unit": "count", "value": 3000})
	o := Options{All: true, Limit: 1}
	page, err := Query(p, "health-data", "list", "", o, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Consumer-side metadata must remain detached from evidence and frozen state.
	metadata := page["sourceSemantics"].(healthdata.FieldSemantics)
	metadata.Sections[0].Rules[0] = "consumer edit"
	o.Cursor = page["nextCursor"].(string)
	if _, err = Query(p, "health-data", "list", "", o, time.Now()); err != nil {
		t.Fatal(err)
	}
	scan, err := Next(p, "health-data", Options{All: true}, true)
	if err != nil || scan["totalPending"] != 2 || hasSection(t, scan, "sleepSessions") {
		t.Fatal(scan, err)
	}
	path, _ := statePath(p, "health-data")
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("scan created checkpoint", err)
	}
	batch, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	batch["sourceSemantics"].(healthdata.FieldSemantics).Sections[0].Rules[0] = "consumer edit"
	replay, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || replay["token"] != batch["token"] {
		t.Fatal("pending changed")
	}
	if replay["sourceSemantics"].(healthdata.FieldSemantics).Sections[0].Rules[0] == "consumer edit" {
		t.Fatal("metadata aliased")
	}
}

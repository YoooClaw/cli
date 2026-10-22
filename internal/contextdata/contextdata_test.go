package contextdata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/paths"
)

func fixture(t *testing.T, p string, v ...any) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	b := []byte{}
	for _, r := range v {
		x, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		b = append(b, x...)
		b = append(b, '\n')
	}
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func event(start, end string) map[string]any {
	return map[string]any{"startAt": start, "endAt": end, "timeZone": "Asia/Shanghai", "title": "synthetic event"}
}
func TestCalendarScopesPaginationAndOverlap(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	now, _ := time.Parse(time.RFC3339, "2026-10-22T12:00:00+08:00")
	past := event("2026-10-22T08:00:00+08:00", "2026-10-22T09:00:00+08:00")
	ongoing := event("2026-10-22T11:00:00+08:00", "2026-10-22T14:00:00+08:00")
	future := event("2026-10-22T13:00:00+08:00", "2026-10-23T14:00:00+08:00")
	tomorrow := event("2026-10-23T15:00:00+08:00", "2026-10-23T16:00:00+08:00")
	fixture(t, filepath.Join(p.Calendars, "2026-10-22.jsonl"), past, ongoing, future)
	fixture(t, filepath.Join(p.Calendars, "2026-10-23.jsonl"), future, tomorrow)
	r, e := Query(p, "calendars", "list", "", Options{Limit: 1}, now)
	if e != nil {
		t.Fatal(e)
	}
	if r["scope"] != "upcoming" || r["total"] != 2 {
		t.Fatal(r)
	}
	token := r["nextCursor"].(string)
	r, e = Query(p, "calendars", "list", "", Options{Limit: 1, Cursor: token}, now.Add(2*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if r["returned"] != 1 {
		t.Fatal(r)
	}
	r, e = Query(p, "calendars", "list", "", Options{All: true, Limit: 100}, now)
	if e != nil || r["total"] != 4 || r["scope"] != "all_synced" {
		t.Fatal(r, e)
	}
	r, e = Query(p, "calendars", "list", "", Options{Date: "2026-10-23", Limit: 100}, now)
	if e != nil || r["total"] != 2 {
		t.Fatal(r, e)
	}
	if _, e = Query(p, "calendars", "list", "", Options{Date: "2026-10-23", All: true, Limit: 100}, now); e == nil {
		t.Fatal("conflicting scope accepted")
	}
	fixture(t, filepath.Join(p.Calendars, "2026-10-23.jsonl"), future)
	if _, e = Query(p, "calendars", "list", "", Options{Limit: 1, Cursor: token}, now); e == nil {
		t.Fatal("changed snapshot accepted")
	}
}
func health(t *testing.T, p paths.Paths, updated string, steps int) {
	fixture(t, filepath.Join(p.HealthData, "2026-10-22.jsonl"), map[string]any{"schemaVersion": 2, "date": "2026-10-22", "timeZone": "Asia/Shanghai", "provider": "test", "updatedAt": updated}, map[string]any{"metric": "steps", "name": "Steps", "value": steps, "unit": "count"})
}
func TestSnapshotCommitDoesNotConsumeConcurrentUpdate(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "one")
	o := Options{All: true}
	health(t, p, "2026-10-22T10:00:00+08:00", 100)
	a, e := Next(p, "health-data", o, false)
	if e != nil {
		t.Fatal(e)
	}
	token := a["token"].(string)
	health(t, p, "2026-10-22T11:00:00+08:00", 200)
	again, e := Next(p, "health-data", o, false)
	if e != nil || again["token"] != token {
		t.Fatal(again, e)
	}
	if _, e = Next(p, "health-data", Options{Date: "2026-10-21"}, false); e == nil {
		t.Fatal("pending scope switched")
	}
	if _, e = Commit(p, "locations", token); e == nil {
		t.Fatal("cross-source token accepted")
	}
	if _, e = Commit(paths.ForRoot(filepath.Dir(filepath.Dir(p.Dir)), "two"), "health-data", token); e == nil {
		t.Fatal("cross-profile token accepted")
	}
	if _, e = Commit(p, "health-data", token); e != nil {
		t.Fatal(e)
	}
	if _, e = Commit(p, "health-data", token); e != nil {
		t.Fatal("idempotent retry", e)
	}
	b, e := Next(p, "health-data", o, false)
	if e != nil || b["done"] != false {
		t.Fatal(b, e)
	}
	changes := b["items"].([]Change)
	if changes[0].PreviousVersion == "" || changes[0].PreviousData == nil {
		t.Fatal(changes)
	}
	if _, e = Commit(p, "health-data", b["token"].(string)); e != nil {
		t.Fatal(e)
	}
	health(t, p, "2026-10-22T12:00:00+08:00", 200)
	b, e = Next(p, "health-data", o, false)
	if e != nil || b["done"] != true {
		t.Fatal("timestamp-only update reprocessed", b, e)
	}
}
func TestCalendarEmptySnapshotCarriesPreviousFacts(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	path := filepath.Join(p.Calendars, "2026-10-22.jsonl")
	fixture(t, path, event("2026-10-22T13:00:00+08:00", "2026-10-22T14:00:00+08:00"))
	a, e := Next(p, "calendars", Options{All: true}, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Commit(p, "calendars", a["token"].(string)); e != nil {
		t.Fatal(e)
	}
	fixture(t, path)
	a, e = Next(p, "calendars", Options{All: true}, false)
	if e != nil || a["done"] != false {
		t.Fatal(a, e)
	}
	c := a["items"].([]Change)[0]
	if c.PreviousData == nil || len(c.Data["events"].([]map[string]any)) != 0 {
		t.Fatal(c)
	}
}
func TestHealthSeriesKeepsMissingAndUnits(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	health(t, p, "2026-10-22T10:00:00+08:00", 100)
	now, _ := time.Parse(time.RFC3339, "2026-10-22T11:00:00+08:00")
	r, e := Query(p, "health-data", "series", "", Options{Metric: "sleep_minutes", Limit: 100}, now)
	if e != nil {
		t.Fatal(e)
	}
	d := r["items"].([]Item)[0].Data
	if d["missing"] != true || d["measurement"] != nil || d["partialDay"] != true {
		t.Fatal(d)
	}
}
func TestLocationDedupAndQueryDoesNotConsume(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	r := map[string]any{"latitude": 0, "longitude": 0, "collectedAt": "unknown"}
	fixture(t, filepath.Join(p.Locations, "2026-10-22.jsonl"), r, r)
	q, e := Query(p, "locations", "list", "", Options{Limit: 100}, time.Now())
	if e != nil || q["total"] != 1 {
		t.Fatal(q, e)
	}
	a, e := Next(p, "locations", Options{All: true}, false)
	if e != nil || a["returned"] != 1 {
		t.Fatal(a, e)
	}
}
func TestCorruptSourceAndCheckpointFailClosed(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	path := filepath.Join(p.Locations, "2026-10-22.jsonl")
	fixture(t, path, map[string]any{"latitude": 0})
	os.WriteFile(path, []byte("{invalid\n"), 0600)
	if _, e := Next(p, "locations", Options{All: true}, false); e == nil {
		t.Fatal("invalid source accepted")
	}
	fixture(t, path, map[string]any{"latitude": 0})
	sp, _ := statePath(p, "locations")
	os.WriteFile(sp, []byte("invalid"), 0600)
	if _, e := Next(p, "locations", Options{All: true}, false); e == nil {
		t.Fatal("invalid checkpoint reset")
	}
}
func TestWebpageVersionAndPathBoundary(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	os.MkdirAll(filepath.Join(p.WebPages, "files"), 0700)
	body := filepath.Join(p.WebPages, "files", "page.md")
	os.WriteFile(body, []byte("---\ncapturedAt: old\n---\nReference knowledge"), 0600)
	entry := map[string]any{"urlHash": "abc", "relativePath": "files/page.md", "canonicalUrl": "https://example.com", "capturedAt": "2026-10-22T10:00:00Z"}
	fixture(t, filepath.Join(p.WebPages, "index.json"), map[string]any{"pages": []any{entry}})
	a, e := Next(p, "web-pages", Options{All: true}, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Commit(p, "web-pages", a["token"].(string)); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(body, []byte("---\ncapturedAt: new\n---\nReference knowledge"), 0600)
	a, e = Next(p, "web-pages", Options{All: true}, false)
	if e != nil || a["done"] != true {
		t.Fatal(a, e)
	}
	os.WriteFile(body, []byte("New content"), 0600)
	a, e = Next(p, "web-pages", Options{All: true}, false)
	if e != nil || a["done"] != false {
		t.Fatal(a, e)
	}
	outside := filepath.Join(p.Dir, "outside.md")
	os.WriteFile(outside, []byte("private"), 0600)
	entry["relativePath"] = "../outside.md"
	fixture(t, filepath.Join(p.WebPages, "index.json"), map[string]any{"pages": []any{entry}})
	if _, e = Read(p, "web-pages"); e == nil {
		t.Fatal("escape accepted")
	}
}

func TestAllDayAndMissingCoverage(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	now, _ := time.Parse(time.RFC3339, "2026-10-22T12:00:00+08:00")
	r, e := Query(p, "calendars", "list", "", Options{Limit: 100}, now)
	if e != nil || r["coverage"].(map[string]any)["connected"] != false {
		t.Fatal(r, e)
	}
	a := event("2026-10-22T16:00:00+08:00", "2026-10-23T00:00:00+08:00")
	a["allDay"] = true
	fixture(t, filepath.Join(p.Calendars, "2026-10-22.jsonl"), a)
	r, e = Query(p, "calendars", "list", "", Options{Limit: 100}, now)
	if e != nil || r["total"] != 0 {
		t.Fatal("all-day should already have started", r, e)
	}
	r, e = Query(p, "calendars", "list", "", Options{Date: "2026-10-23", Limit: 100}, now)
	if e != nil || r["total"] != 0 {
		t.Fatal("midnight end is exclusive", r, e)
	}
}
func TestMemoryProcessLockReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, e := lock(path)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := lock(path); e == nil {
		second()
		release()
		t.Fatal("concurrent writer lock accepted")
	}
	release()
	second, e := lock(path)
	if e != nil {
		t.Fatal(e)
	}
	second()
}

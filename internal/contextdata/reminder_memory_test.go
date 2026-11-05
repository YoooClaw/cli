package contextdata

import (
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"testing"
)

func TestReminderSourceChangeDoesNotRepeatMemory(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "test")
	current := Item{ID: "old-record", Date: "1997-07-16", Data: map[string]any{"id": "system-id", "title": "Task", "sourceId": "phone"}}
	current.Version = Hash(current.Data)
	read := func() (Dataset, error) { return Dataset{Connected: true, Items: []Item{current}}, nil }
	next := func() map[string]any {
		t.Helper()
		r, e := NextWithReader(p, "reminders", Options{All: true}, false, read)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first := next()
	// A pending batch is frozen even if storage identity changes before ACK.
	current = Item{ID: "merged-record", Date: "1997-07-16", Ref: "/new-detail", Data: map[string]any{"id": "system-id", "title": "Task", "sourceId": "tablet", "sourceIds": []string{"phone", "tablet"}}}
	current.Version = Hash(current.Data)
	if next()["token"] != first["token"] {
		t.Fatal("pending batch changed")
	}
	if _, e := Commit(p, "reminders", first["token"].(string)); e != nil {
		t.Fatal(e)
	}
	if next()["done"] != true {
		t.Fatal("source-only update repeated")
	}
	current.Data["title"] = "Changed task"
	current.Version = Hash(current.Data)
	changed := next()
	rows := changed["items"].([]Change)
	if len(rows) != 1 || rows[0].ID != "old-record" || rows[0].PreviousData["title"] != "Task" || rows[0].Ref != "/new-detail" {
		t.Fatal(rows)
	}
	if _, e := Commit(p, "reminders", changed["token"].(string)); e != nil {
		t.Fatal(e)
	}
	if next()["done"] != true {
		t.Fatal("committed change repeated")
	}
}

func TestReminderLegacyCheckpointUsesSystemIdentity(t *testing.T) {
	old := Item{ID: "legacy", Data: map[string]any{"id": "system", "title": "Task", "sourceId": "old"}}
	old.Version = Hash(old.Data)
	current := Item{ID: "new", Data: map[string]any{"id": "system", "title": "Task", "sourceId": "new"}}
	item, previous := reminderConsumption(current, reminderHistory(map[string]Item{old.ID: old}))
	if item.ID != old.ID || item.Version != previous.Version {
		t.Fatal(item, previous)
	}
}

func TestConflictingLegacyAliasesDoNotSuppressReopening(t *testing.T) {
	a := Item{ID: "a", Data: map[string]any{"id": "same", "title": "Task", "isCompleted": false}}
	b := Item{ID: "b", Data: map[string]any{"id": "same", "title": "Task", "isCompleted": true}}
	item, old := reminderConsumption(Item{ID: "b", Data: a.Data}, reminderHistory(map[string]Item{"a": a, "b": b}))
	if item.Version == old.Version || item.ID != "b" {
		t.Fatal("historical duplicate suppressed reopening", item, old)
	}
}

func TestLegacyConflictCommitConsolidatesCheckpoint(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "test")
	a := Item{ID: "a", Date: "1997-07-16", Data: map[string]any{"id": "same", "isCompleted": false}}
	b := Item{ID: "b", Date: a.Date, Data: map[string]any{"id": "same", "isCompleted": true}}
	path, _ := statePath(p, "reminders")
	if e := fsutil.WriteJSON(path, memoryState{Seen: map[string]Item{"a": a, "b": b}}, fsutil.SecretFileMode); e != nil {
		t.Fatal(e)
	}
	current := Item{ID: "b", Date: a.Date, Data: a.Data}
	read := func() (Dataset, error) { return Dataset{Connected: true, Items: []Item{current}}, nil }
	next, e := NextWithReader(p, "reminders", Options{All: true}, false, read)
	if e != nil || next["done"] != false {
		t.Fatal(next, e)
	}
	if _, e = Commit(p, "reminders", next["token"].(string)); e != nil {
		t.Fatal(e)
	}
	state, e := loadState(path)
	if e != nil || len(state.Seen) != 1 {
		t.Fatal(state, e)
	}
	next, e = NextWithReader(p, "reminders", Options{All: true}, false, read)
	if e != nil || next["done"] != true {
		t.Fatal("reconciled version repeated", next, e)
	}
}

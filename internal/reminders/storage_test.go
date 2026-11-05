package reminders

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func sample(id string) Item {
	return Item{Summary: Summary{ID: id, Title: "same title", ListID: "list", ListName: "Personal"}, Notes: ptr("private note")}
}

func payload(source, when string, items ...Item) Payload {
	if items == nil {
		items = []Item{}
	}
	return Payload{SchemaVersion: 1, SourceID: source, CollectedAt: "2026-10-01T" + when + "+08:00", Items: items}
}

func replace(t *testing.T, s *Storage, p Payload) Result {
	t.Helper()
	r, err := s.Replace(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func view(t *testing.T, s *Storage) (Index, State) {
	t.Helper()
	var index Index
	var state State
	err := s.View(func(i Index, v State, _ func(string) (Record, error)) error { index, state = i, v; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return index, state
}

func TestCurrentMirrorTransitionsAndSourceIsolation(t *testing.T) {
	s := NewStorage(t.TempDir())
	a, b, c := sample("a"), sample("b"), sample("c")
	a.IsRecurring = true
	replace(t, s, payload("phone", "09:00:00", a, b))
	replace(t, s, payload("tablet", "09:00:00", a, c))
	before, _ := view(t, s)
	if len(before.Items) != 3 {
		t.Fatalf("want three distinct IDs, got %+v", before.Items)
	}
	var aRecordID string
	for _, entry := range before.Items {
		if entry.ID == "a" {
			aRecordID = entry.RecordID
		}
	}
	a.IsCompleted, a.CompletedAt = true, ptr("2026-09-30T23:59:59-07:00")
	a.Notes = ptr("updated")
	replace(t, s, payload("phone", "10:00:00", a)) // b disappears only from phone.
	idx, _ := view(t, s)
	if len(idx.Items) != 2 {
		t.Fatal("other source or duplicate title lost")
	}
	if err := s.View(func(_ Index, _ State, detail func(string) (Record, error)) error {
		r, err := detail(aRecordID)
		if err != nil || r.ID != "a" || r.SourceID != "phone" || partition(r) != "completed/2026-09-30.jsonl" ||
			r.Notes == nil || *r.Notes != "updated" || strings.Join(r.SourceIDs, ",") != "phone,tablet" {
			t.Fatal(r, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.CompletedAt = nil
	replace(t, s, payload("phone", "11:00:00", a))
	if _, err := os.Stat(filepath.Join(s.dir, "completed/2026-09-30.jsonl")); !os.IsNotExist(err) {
		t.Fatal("old completion partition remained")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "completed/undated.jsonl")); err != nil {
		t.Fatal(err)
	}
	a.IsCompleted = false
	replace(t, s, payload("phone", "12:00:00", a))
	replace(t, s, payload("phone", "13:00:00"))
	idx, state := view(t, s)
	if len(idx.Items) != 2 || len(state.Sources) != 2 {
		t.Fatal("source mirror isolation")
	}
	for _, e := range idx.Items {
		if e.ID == "a" && (e.RecordID != aRecordID || strings.Join(e.SourceIDs, ",") != "tablet") {
			t.Fatal("omitting source removed or changed shared item", e)
		}
	}
	replace(t, s, payload("tablet", "10:00:00", c))
	idx, _ = view(t, s)
	if len(idx.Items) != 1 || idx.Items[0].ID != "c" {
		t.Fatal("omission from all sources did not remove shared item")
	}
}

func TestStaleIdempotencyAndNotesOnlyRevision(t *testing.T) {
	s := NewStorage(t.TempDir())
	a, b := sample("a"), sample("b")
	first := replace(t, s, payload("phone", "09:00:00", a, b))
	second := replace(t, s, payload("phone", "10:00:00", b, a))
	if first.Revision != second.Revision || !second.Applied {
		t.Fatal("time/order-only sync changed revision")
	}
	a.Notes = ptr("changed")
	stale := replace(t, s, payload("phone", "09:00:00", a))
	equal := replace(t, s, payload("phone", "10:00:00", a))
	if stale.Applied || equal.Applied || stale.Revision != first.Revision {
		t.Fatal("stale snapshot applied")
	}
	changed := replace(t, s, payload("phone", "11:00:00", a, b))
	if changed.Revision == first.Revision {
		t.Fatal("notes did not invalidate detail/list revision")
	}
	if err := s.View(func(_ Index, _ State, detail func(string) (Record, error)) error {
		r, err := detail(itemRecordID("a"))
		if err != nil || r.Notes == nil || *r.Notes != "changed" {
			t.Fatal(r, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAcceptedSnapshotConsolidatesLegacyDuplicates(t *testing.T) {
	s := NewStorage(t.TempDir())
	a, b := sample("a"), sample("b")
	newB := b
	newB.Title = "tablet version"
	rows := []Record{
		{Item: a, SourceID: "phone", RecordID: RecordID("phone", a.ID)},
		{Item: a, SourceID: "tablet", RecordID: RecordID("tablet", a.ID)},
		{Item: b, SourceID: "phone", RecordID: RecordID("phone", b.ID)},
		{Item: newB, SourceID: "tablet", RecordID: RecordID("tablet", b.ID)},
	}
	idx, files, err := makeSnapshot(rows)
	if err != nil {
		t.Fatal(err)
	}
	state := State{SchemaVersion: SchemaVersion, Sources: map[string]SourceState{
		"phone":  {LastCollectedAt: "2026-10-01T17:00:00+08:00"},
		"tablet": {LastCollectedAt: "2026-10-01T10:00:00Z"},
	}}
	indexRaw, _ := json.Marshal(idx)
	stateRaw, _ := json.Marshal(state)
	files["index.json"], files["state.json"] = string(indexRaw), string(stateRaw)
	if err := s.commit(transaction{Files: files}); err != nil {
		t.Fatal(err)
	}
	a.Title = "reinstalled version"
	replace(t, s, payload("reinstall", "19:00:00", a))
	raw, err := s.read("active.jsonl", StoreLimit/2)
	if err != nil || strings.Count(string(raw), "\n") != 2 {
		t.Fatal("legacy duplicate rows still on disk", err)
	}
	if err := os.Remove(filepath.Join(s.dir, "index.json")); err != nil {
		t.Fatal(err)
	}
	idx, _ = view(t, NewStorage(s.dir))
	if len(idx.Items) != 2 {
		t.Fatal("index rebuild lost consolidated rows", idx.Items)
	}
	for _, e := range idx.Items {
		switch e.ID {
		case "a":
			if e.Title != a.Title || e.SourceID != "reinstall" || strings.Join(e.SourceIDs, ",") != "phone,reinstall,tablet" {
				t.Fatal("incoming data did not overwrite legacy copies", e)
			}
		case "b":
			if e.Title != newB.Title || e.SourceID != "tablet" || strings.Join(e.SourceIDs, ",") != "phone,tablet" {
				t.Fatal("legacy rows did not use the newer source watermark", e)
			}
		default:
			t.Fatal("unexpected reminder", e)
		}
	}
}

func TestEmptyFirstSnapshotAndMissingIndexRebuild(t *testing.T) {
	s := NewStorage(t.TempDir())
	first := replace(t, s, payload("phone", "09:00:00"))
	for _, name := range []string{"active.jsonl", "index.json", "state.json", "README.md"} {
		if _, err := os.Stat(filepath.Join(s.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if !first.Applied || first.Ingested != 0 {
		t.Fatal(first)
	}
	next := replace(t, s, payload("phone", "10:00:00", sample("a")))
	if err := os.Remove(filepath.Join(s.dir, "index.json")); err != nil {
		t.Fatal(err)
	}
	idx, state := view(t, NewStorage(s.dir))
	if idx.Revision != next.Revision || len(idx.Items) != 1 || state.Sources["phone"].LastCollectedAt != "2026-10-01T10:00:00+08:00" {
		t.Fatal("rebuild lost current data")
	}
}

func TestInterruptedReplacementRecoversBeforeRead(t *testing.T) {
	s := NewStorage(t.TempDir())
	replace(t, s, payload("phone", "09:00:00", sample("old")))
	row := Record{Item: sample("new"), SourceID: "phone", RecordID: RecordID("phone", "new")}
	idx, files, err := makeSnapshot([]Record{row})
	if err != nil {
		t.Fatal(err)
	}
	indexRaw, _ := json.Marshal(idx)
	stateRaw, _ := json.Marshal(State{SchemaVersion: 1, Sources: map[string]SourceState{"phone": {LastCollectedAt: "2026-10-01T10:00:00+08:00"}}})
	files["index.json"], files["state.json"] = string(indexRaw), string(stateRaw)
	tx, _ := json.Marshal(transaction{Files: files})
	if err := s.write(".pending.json", tx); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after one partition was replaced, before index/state.
	if err := s.write("active.jsonl", []byte(files["active.jsonl"])); err != nil {
		t.Fatal(err)
	}
	got, state := view(t, NewStorage(s.dir))
	if got.Revision != idx.Revision || got.Items[0].ID != "new" || state.Sources["phone"].LastCollectedAt != "2026-10-01T10:00:00+08:00" {
		t.Fatal("mixed snapshot served")
	}
	if _, err := os.Stat(filepath.Join(s.dir, ".pending.json")); !os.IsNotExist(err) {
		t.Fatal("journal not finished")
	}
}

func TestDecodeRejectsPartialAndInvalidSnapshots(t *testing.T) {
	valid, _ := json.Marshal(payload("phone", "09:00:00", sample("a")))
	var body map[string]any
	if err := json.Unmarshal(valid, &body); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(map[string]any){
		func(p map[string]any) { delete(p, "items") },
		func(p map[string]any) { p["items"] = nil },
		func(p map[string]any) { p["schemaVersion"] = 2 },
		func(p map[string]any) { p["collectedAt"] = "2026-10-01" },
		func(p map[string]any) { delete(p["items"].([]any)[0].(map[string]any), "isCompleted") },
		func(p map[string]any) { p["items"] = append(p["items"].([]any), p["items"].([]any)[0]) },
		func(p map[string]any) { p["items"].([]any)[0].(map[string]any)["priority"] = 10 },
		func(p map[string]any) {
			p["items"].([]any)[0].(map[string]any)["due"] = map[string]any{"date": "2026-02-30"}
		},
		func(p map[string]any) {
			p["items"].([]any)[0].(map[string]any)["notes"] = strings.Repeat("x", RecordLimit)
		},
	} {
		copy := map[string]any{}
		_ = json.Unmarshal(valid, &copy)
		mutation(copy)
		raw, _ := json.Marshal(copy)
		if _, err := Decode(raw); err == nil {
			t.Fatalf("accepted invalid snapshot: %.120s", raw)
		}
	}
	dateOnly := sample("dated")
	dateOnly.Due = &Due{Date: "2026-10-01"}
	raw, _ := json.Marshal(payload("phone", "09:00:00", dateOnly))
	p, err := Decode(raw)
	if err != nil || p.Items[0].Due.Time != nil || p.Items[0].Due.TimeZone != nil {
		t.Fatal("date-only/floating semantics changed")
	}
}

func TestFailedSnapshotDoesNotClearCurrentMirror(t *testing.T) {
	s := NewStorage(t.TempDir())
	first := replace(t, s, payload("phone", "09:00:00", sample("a")))
	p := payload("phone", "10:00:00")
	p.Items = nil
	if _, err := s.Replace(p); err == nil {
		t.Fatal("nil snapshot applied")
	}
	idx, _ := view(t, s)
	if idx.Revision != first.Revision || len(idx.Items) != 1 {
		t.Fatal("failed snapshot changed data")
	}
}

func TestMalformedMetadataFailsClosed(t *testing.T) {
	for _, file := range []string{"index.json", "state.json"} {
		for _, content := range []string{"{}", "null", "{broken"} {
			t.Run(file+content, func(t *testing.T) {
				s := NewStorage(t.TempDir())
				replace(t, s, payload("phone", "09:00:00", sample("a")))
				if err := s.write(file, []byte(content)); err != nil {
					t.Fatal(err)
				}
				if err := s.View(func(Index, State, func(string) (Record, error)) error {
					t.Fatal("corrupt metadata served as empty")
					return nil
				}); err == nil {
					t.Fatal("corrupt metadata accepted")
				}
				if _, err := s.Replace(payload("phone", "10:00:00")); err == nil {
					t.Fatal("replaced mirror without valid metadata")
				}
			})
		}
	}
}

func TestCrossSourceDelayedSnapshotPreservesNewerContent(t *testing.T) {
	s := NewStorage(t.TempDir())
	a := sample("shared")
	a.IsCompleted = true
	replace(t, s, payload("phone", "12:00:00", a))
	a.IsCompleted = false
	replace(t, s, payload("tablet", "10:00:00", a))
	idx, _ := view(t, s)
	if len(idx.Items) != 1 || !idx.Items[0].IsCompleted || len(idx.Items[0].SourceIDs) != 2 {
		t.Fatal(idx)
	}
	// Removing the content's source updates visibility, not content freshness.
	replace(t, s, payload("phone", "14:00:00"))
	s = NewStorage(s.dir)
	replace(t, s, payload("tablet", "13:00:00", a))
	idx, _ = view(t, s)
	if len(idx.Items) != 1 || idx.Items[0].IsCompleted {
		t.Fatal("newer content rejected after source disappearance", idx)
	}
}

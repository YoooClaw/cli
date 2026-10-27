package reminders

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReplacementRecoveryAtEveryCommitBoundary(t *testing.T) {
	for _, phase := range []string{"journal", "partitions", "deletions", "index", "state"} {
		t.Run(phase, func(t *testing.T) {
			s := NewStorage(t.TempDir())
			old := sample("old")
			old.IsCompleted, old.CompletedAt = true, ptr("2026-09-30T10:00:00Z")
			replace(t, s, payload("phone", "09:00:00", old))
			replace(t, s, payload("tablet", "09:00:00", sample("tablet")))
			_, state := view(t, s)
			rows := []Record{
				{Item: sample("new"), SourceID: "phone", RecordID: RecordID("phone", "new")},
				{Item: sample("tablet"), SourceID: "tablet", RecordID: RecordID("tablet", "tablet")},
			}
			idx, files, err := makeSnapshot(rows)
			if err != nil {
				t.Fatal(err)
			}
			state.Sources["phone"] = SourceState{LastCollectedAt: "2026-10-01T10:00:00+08:00"}
			indexRaw, _ := json.Marshal(idx)
			stateRaw, _ := json.Marshal(state)
			files["index.json"], files["state.json"] = string(indexRaw), string(stateRaw)
			oldFile := "completed/2026-09-30.jsonl"
			tx, _ := json.Marshal(transaction{Files: files, Remove: []string{oldFile}})
			if err := s.write(".pending.json", tx); err != nil {
				t.Fatal(err)
			}
			steps := []struct {
				name string
				run  func() error
			}{
				{"partitions", func() error { return s.write("active.jsonl", []byte(files["active.jsonl"])) }},
				{"deletions", func() error { return os.Remove(filepath.Join(s.dir, oldFile)) }},
				{"index", func() error { return s.write("index.json", indexRaw) }},
				{"state", func() error { return s.write("state.json", stateRaw) }},
			}
			if phase != "journal" {
				for _, step := range steps {
					if err := step.run(); err != nil {
						t.Fatal(err)
					}
					if step.name == phase {
						break
					}
				}
			}
			reopened := NewStorage(s.dir)
			got, recovered := view(t, reopened)
			if got.Revision != idx.Revision || len(got.Items) != 2 || recovered.Sources["phone"] != state.Sources["phone"] {
				t.Fatal("mixed snapshot or watermark after recovery")
			}
			if err := reopened.View(func(_ Index, _ State, detail func(string) (Record, error)) error {
				for _, row := range rows {
					stored, err := detail(row.RecordID)
					if err != nil || stored.ID != row.ID || stored.SourceID != row.SourceID {
						t.Fatal("source lost or detail inconsistent", stored, err)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{oldFile, ".pending.json"} {
				if _, err := os.Stat(filepath.Join(s.dir, name)); !os.IsNotExist(err) {
					t.Fatal("transaction left old partition or journal", name, err)
				}
			}
			if result := replace(t, reopened, payload("phone", "09:00:00", old)); result.Applied {
				t.Fatal("recovery lost the stale-snapshot guard")
			}
			if err := os.Remove(filepath.Join(s.dir, "index.json")); err != nil {
				t.Fatal(err)
			}
			rebuilt, _ := view(t, NewStorage(s.dir))
			if rebuilt.Revision != idx.Revision || len(rebuilt.Items) != 2 {
				t.Fatal("index rebuild resurrected replaced records")
			}
		})
	}
}

func TestIndexRebuildRejectsMissingWatermarkOrPartitions(t *testing.T) {
	for _, missing := range []string{"watermark", "partitions"} {
		t.Run(missing, func(t *testing.T) {
			s := NewStorage(t.TempDir())
			replace(t, s, payload("phone", "09:00:00", sample("a")))
			if err := os.Remove(filepath.Join(s.dir, "index.json")); err != nil {
				t.Fatal(err)
			}
			if missing == "watermark" {
				if err := s.write("state.json", []byte(`{"schemaVersion":1,"sources":{}}`)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(s.dir, "active.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := s.View(func(Index, State, func(string) (Record, error)) error {
				t.Fatal("incomplete persisted snapshot was served")
				return nil
			}); err == nil {
				t.Fatal("incomplete persisted snapshot was accepted")
			}
			if _, err := s.read("index.json", 32<<20); !os.IsNotExist(err) {
				t.Fatal("inconsistent derived index was published", err)
			}
		})
	}
}

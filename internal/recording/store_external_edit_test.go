package recording

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rewriteIndex 模拟外部进程（人工修复、迁移脚本等）直接改写 index.json，
// 并把 mtime 挪开，避免与 daemon 上次记录的文件身份相同。
func rewriteIndex(t *testing.T, s *Storage, mutate func(*indexWrapper)) {
	t.Helper()
	path := filepath.Join(s.dir, "index.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wrapper indexWrapper
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatal(err)
	}
	mutate(&wrapper)
	data, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestStoreWriteHonorsExternalIndexDeletion(t *testing.T) {
	s := newStore(t)
	if _, err := s.Ingest("r1", meta("一", "2026-06-01T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest("r2", meta("二", "2026-06-02T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}

	rewriteIndex(t, s, func(w *indexWrapper) {
		kept := w.Recordings[:0]
		for _, entry := range w.Recordings {
			if entry.ID != "r1" {
				kept = append(kept, entry)
			}
		}
		w.Recordings = kept
	})

	if _, ok, err := s.Rename("r2", "新名字"); err != nil || !ok {
		t.Fatalf("rename after external edit: ok=%v err=%v", ok, err)
	}
	if _, ok := s.FindByID("r1"); ok {
		t.Fatal("externally deleted r1 was resurrected by the next write")
	}
	disk := ReadIndex(s.dir)
	if len(disk) != 1 || disk[0].ID != "r2" || disk[0].Metadata.Name != "新名字" {
		t.Fatalf("disk index after write = %+v", disk)
	}
}

func TestStoreReadSeesExternalIndexDeletion(t *testing.T) {
	s := newStore(t)
	if _, err := s.Ingest("r1", meta("一", "2026-06-01T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest("r2", meta("二", "2026-06-02T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}

	rewriteIndex(t, s, func(w *indexWrapper) {
		kept := w.Recordings[:0]
		for _, entry := range w.Recordings {
			if entry.ID != "r1" {
				kept = append(kept, entry)
			}
		}
		w.Recordings = kept
	})

	if _, ok := s.FindByID("r1"); ok {
		t.Fatal("status lookup still returns an externally deleted recording")
	}
	if all := s.ListAll(); len(all) != 1 || all[0].ID != "r2" {
		t.Fatalf("ListAll after external edit = %+v", all)
	}
}

func TestStoreWritePicksUpExternalIndexAddition(t *testing.T) {
	s := newStore(t)
	if _, err := s.Ingest("r1", meta("一", "2026-06-01T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}

	rewriteIndex(t, s, func(w *indexWrapper) {
		w.Recordings = append(w.Recordings, Entry{
			ID:         "external",
			Status:     StatusSynced,
			Metadata:   meta("外部写入", "2026-06-03T00:00:00Z"),
			IngestedAt: nowISO(),
			UpdatedAt:  nowISO(),
		})
	})

	if _, ok, err := s.Rename("r1", "新名字"); err != nil || !ok {
		t.Fatalf("rename after external edit: ok=%v err=%v", ok, err)
	}
	if _, ok := s.FindByID("external"); !ok {
		t.Fatal("externally added entry was lost by the next write")
	}
	if disk := ReadIndex(s.dir); len(disk) != 2 {
		t.Fatalf("disk index after write = %+v", disk)
	}
}

func TestStoreKeepsMemoryWhenExternalIndexIsUnreadable(t *testing.T) {
	s := newStore(t)
	if _, err := s.Ingest("r1", meta("一", "2026-06-01T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(s.dir, "index.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Rename("r1", "新名字"); err != nil || !ok {
		t.Fatalf("rename with unreadable external index: ok=%v err=%v", ok, err)
	}
	if entry, ok := s.FindByID("r1"); !ok || entry.Metadata.Name != "新名字" {
		t.Fatalf("in-memory entry lost after unreadable external index: %+v ok=%v", entry, ok)
	}
	disk := ReadIndex(s.dir)
	if len(disk) != 1 || disk[0].ID != "r1" || disk[0].Metadata.Name != "新名字" {
		t.Fatalf("disk index was not repaired from memory: %+v", disk)
	}
}

func TestStoreWriteWithoutExternalEditKeepsState(t *testing.T) {
	s := newStore(t)
	if _, err := s.Ingest("r1", meta("一", "2026-06-01T00:00:00Z"), "p"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Rename("r1", "新名字"); err != nil || !ok {
		t.Fatalf("rename: ok=%v err=%v", ok, err)
	}
	if entry, ok := s.FindByID("r1"); !ok || entry.Metadata.Name != "新名字" {
		t.Fatalf("entry after rename: %+v ok=%v", entry, ok)
	}
	if disk := ReadIndex(s.dir); len(disk) != 1 || disk[0].Metadata.Name != "新名字" {
		t.Fatalf("disk index after rename = %+v", disk)
	}
}

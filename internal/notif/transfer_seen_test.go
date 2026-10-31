package notif

import (
	"github.com/YoooClaw/cli/internal/testutil"
	"testing"
)

func TestTransferSeenSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	logger := testutil.Logger{T: t}
	s := NewStorage(dir, PluginConfig{}, logger)
	if e := s.Init(); e != nil {
		t.Fatal(e)
	}
	raw := RawNotification{ID: "deleted", App: "chat", Title: "old", Body: "deleted body", Timestamp: "2026-10-26T08:00:00+08:00"}
	entry := s.buildStored(raw, "default")
	day, _ := DateKeyOf(raw.Timestamp)
	if e := s.ImportTransferSeen(day, []string{"deleted"}, []string{contentKey(entry)}); e != nil {
		t.Fatal(e)
	}
	s = NewStorage(dir, PluginConfig{}, logger)
	if e := s.Init(); e != nil {
		t.Fatal(e)
	}
	if r := s.Ingest([]RawNotification{raw}, "default"); r.DedupedByID != 1 {
		t.Fatalf("deleted id revived: %+v", r)
	}
	raw.ID = "new-upstream-id"
	if r := s.Ingest([]RawNotification{raw}, "default"); r.DedupedByContent != 1 {
		t.Fatalf("deleted content revived: %+v", r)
	}
	if e := s.ImportDay(day, func(existing []StoredNotification, admit func(StoredNotification) bool) error {
		if admit(entry) {
			t.Error("import revived deleted content")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

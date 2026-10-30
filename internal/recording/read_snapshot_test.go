package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadIndexSnapshotDoesNotNormalizeOrOpenArtifacts(t *testing.T) {
	s := newStore(t)
	wrapper := indexWrapper{Recordings: []Entry{{ID: "one", Metadata: Metadata{Name: "raw", CreatedAt: "2026-10-30T10:00:00Z"},
		Status: "transcribing", AudioFile: "missing-audio", TranscriptFile: "missing-transcript"}}}
	raw, _ := json.Marshal(wrapper)
	if err := os.WriteFile(s.indexPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ReadIndexSnapshot(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Status != "transcribing" || entries[0].AudioFile != "missing-audio" {
		t.Fatalf("snapshot changed committed metadata: %+v %v", entries, err)
	}
	after, _ := os.ReadFile(s.indexPath)
	if !bytes.Equal(raw, after) {
		t.Fatal("snapshot read rewrote index")
	}
}

func TestReadIndexSnapshotErrorsAndMissingIndex(t *testing.T) {
	s := newStore(t)
	entries, err := s.ReadIndexSnapshot(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty snapshot: %+v %v", entries, err)
	}
	for _, raw := range []string{"broken", "{}", `{"recordings":null}`, `{"recordings":{}}`} {
		if err := os.WriteFile(s.indexPath, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadIndexSnapshot(context.Background()); err == nil {
			t.Fatalf("malformed index became empty: %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadIndexSnapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled snapshot: %v", err)
	}
	if err := os.Truncate(s.indexPath, SummaryIndexLimit+1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadIndexSnapshot(context.Background()); !errors.Is(err, ErrSummaryIndexTooLarge) {
		t.Fatalf("oversized index: %v", err)
	}
}

func TestReadIndexSnapshotRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	s := newStore(t)
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"recordings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.indexPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadIndexSnapshot(context.Background()); err == nil {
		t.Fatal("symlinked recording index accepted")
	}
}

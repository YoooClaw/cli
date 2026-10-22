package healthdata

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOldHealthFormatsAreRejectedWithoutMigration(t *testing.T) {
	for _, raw := range []string{`{"items":[]}`, `{"schemaVersion":1,"items":[]}`, strings.Replace(dailyBody, `"schemaVersion":3`, `"schemaVersion":2`, 1), `null`, `[]`, dailyBody + dailyBody} {
		if _, err := Prepare([]byte(raw)); err == nil {
			t.Fatalf("accepted old or malformed format: %s", raw)
		}
	}
}
func TestSnapshotsPreserveLargeNumbersPermissionsAndUnrelatedHistory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "health-data")
	batch, err := Prepare([]byte(strings.Replace(dailyBody, "31502", "9007199254740993", 1)))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStorage(dir)
	if err = store.Append(batch); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-09-30.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) {
		t.Fatal("number changed", err)
	}
	historical := []byte(`{"schemaVersion":2,"date":"2020-01-01"}`)
	oldPath := filepath.Join(dir, "2020-01-01.jsonl")
	if err = os.WriteFile(oldPath, historical, 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Append(batch); err != nil {
		t.Fatal(err)
	}
	kept, _ := os.ReadFile(oldPath)
	if !bytes.Equal(kept, historical) {
		t.Fatal("unrelated history changed")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("private file mode lost")
		}
	}
}

func TestDailyWriteRejectsOldCollidingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-30.jsonl")
	old := []byte(`{"schemaVersion":2,"date":"2026-09-30"}`)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare([]byte(dailyBody))
	if err != nil {
		t.Fatal(err)
	}
	if err = NewStorage(dir).Append(batch); err == nil {
		t.Fatal("old format overwritten implicitly")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(old, after) {
		t.Fatal("old data changed")
	}
}

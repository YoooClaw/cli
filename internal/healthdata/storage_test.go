package healthdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrepareChecksOnlyContainerStructure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		body    string
		items   int
		deleted int
		valid   bool
	}{
		{"opaque fields", `{"items":[{}, {"data":{"number":1e999,"large":9007199254740993}}],"provider":"future","extra":null}`, 2, 0, true},
		{"empty batch", `{"items":[]}`, 0, 0, true},
		{"deletions only", `{"items":[],"deletedIds":["a","b"]}`, 0, 2, true},
		{"mixed", `{"items":[{}],"deletedIds":["a"]}`, 1, 1, true},
		{"empty deletion list", `{"items":[],"deletedIds":[]}`, 0, 0, true},
		{"empty", ``, 0, 0, false},
		{"null", `null`, 0, 0, false},
		{"array root", `[]`, 0, 0, false},
		{"missing items", `{"deletedIds":["a"]}`, 0, 0, false},
		{"null items", `{"items":null}`, 0, 0, false},
		{"object items", `{"items":{}}`, 0, 0, false},
		{"null record", `{"items":[null]}`, 0, 0, false},
		{"array record", `{"items":[[]]}`, 0, 0, false},
		{"string record", `{"items":["not-an-object"]}`, 0, 0, false},
		{"null deletion list", `{"items":[],"deletedIds":null}`, 0, 0, false},
		{"number deletion", `{"items":[],"deletedIds":[1]}`, 0, 0, false},
		{"null deletion", `{"items":[],"deletedIds":[null]}`, 0, 0, false},
		{"multiple values", `{"items":[]} {"items":[]}`, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch, err := Prepare(json.RawMessage(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("Prepare() error=%v, want valid=%v", err, tc.valid)
			}
			if tc.valid && (batch.ItemCount != tc.items || batch.DeletedCount != tc.deleted) {
				t.Fatalf("counts=%d/%d, want %d/%d", batch.ItemCount, batch.DeletedCount, tc.items, tc.deleted)
			}
		})
	}
}

func TestAppendPreservesWholePayloadWithoutMetadataOrNumberConversion(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"schemaVersion": 1, "provider": "future-provider", "collectedAt": "2020-01-01T00:00:00+08:00",
		"items": [{"data":{"large":9007199254740993,"huge":1e999,"text":"line\n\u4F60好"},"unknown":[true,null]}],
		"deletedIds": ["old-id"], "future": {"key": 1.000}
	}`)
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "health-data")
	storage := NewStorage(dir)
	for range 2 {
		if err := storage.Append(batch); err != nil {
			t.Fatal(err)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	line := append(compact.Bytes(), '\n')
	path := filepath.Join(dir, "legacy", "receipts", time.Now().Format("2006-01-02")+".jsonl")
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, bytes.Repeat(line, 2)) {
		t.Fatalf("payload was changed or deduplicated: %s, err=%v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2020-01-01.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("must partition by receipt day, not collectedAt: %v", err)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("permissions: path=%s info=%v err=%v", path, info, err)
			}
		}
	}
}

func TestConcurrentAppendKeepsEachBatchOnItsOwnLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storage := NewStorage(dir)
	const count = 40
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			batch, err := Prepare(json.RawMessage(fmt.Sprintf(`{"items":[{"id":%d}]}`, i)))
			if err == nil {
				err = storage.Append(batch)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "legacy", "receipts", time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	seen := map[int]bool{}
	for _, line := range lines {
		var body struct{ Items []struct{ ID int } }
		if err := json.Unmarshal(line, &body); err != nil || len(body.Items) != 1 {
			t.Fatalf("invalid/interleaved JSONL: %s, err=%v", line, err)
		}
		seen[body.Items[0].ID] = true
	}
	if len(lines) != count || len(seen) != count {
		t.Fatalf("stored %d lines, %d IDs, want %d", len(lines), len(seen), count)
	}
}

func TestAppendSupportsLargeRecordsAndExistingFileWithoutFinalNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy", "receipts", time.Now().Format("2006-01-02")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"items":[],"deletedIds":["keep-this-event"]}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{"data": strings.Repeat("x", 100_000)}}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStorage(dir).Append(batch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	want := append(append(append([]byte{}, old...), '\n'), append(raw, '\n')...)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("append did not preserve the original line and large batch: bytes=%d err=%v", len(got), err)
	}
}

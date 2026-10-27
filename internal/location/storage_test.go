package location

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestConcurrentAppendPreservesJSONLines(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "locations")
	storage := NewStorage(dir)
	const count = 30
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _ := json.Marshal(i)
			errs <- storage.Append([]Record{{"longitude": json.RawMessage("0"), "latitude": json.RawMessage("0"), "id": id}})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seen := map[int]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var item struct{ ID int }
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatalf("invalid JSONL line: %v", err)
		}
		seen[item.ID] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != count {
		t.Fatalf("stored %d unique records, want %d", len(seen), count)
	}
	if runtime.GOOS != "windows" {
		info, err := f.Stat()
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions: info=%v err=%v", info, err)
		}
		info, err = os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory permissions: info=%v err=%v", info, err)
		}
	}
}

func TestAppendAfterPartialLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(path, []byte(`{"longitude":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewStorage(dir).Append([]Record{{"longitude": json.RawMessage("0"), "latitude": json.RawMessage("0")}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"longitude\":\n{\"latitude\":0,\"longitude\":0}\n"
	if string(raw) != want {
		t.Fatalf("file = %q, want %q", raw, want)
	}
}

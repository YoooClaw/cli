package recording

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Striped locks bound memory and serialize result writers for the same recording.
var contentLocks [64]sync.Mutex

func contentSnapshot(s *Storage, id string) string {
	entry, ok := s.FindByID(id)
	if !ok {
		return ""
	}
	h := sha256.New()
	count := 0
	for _, item := range []struct{ dir, name string }{{s.TranscriptDataDir(), entry.TranscriptDataFile}, {s.TranscriptsDir(), entry.TranscriptFile}, {s.SummariesDir(), entry.SummaryFile}} {
		if item.name == "" {
			continue
		}
		f, e := os.Open(filepath.Join(s.dir, item.name))
		if e != nil {
			continue
		}
		fmt.Fprint(h, item.name, "\x00")
		if filepath.Ext(item.name) == ".json" {
			raw, readErr := io.ReadAll(f)
			e = readErr
			var doc map[string]any
			if e == nil && json.Unmarshal(raw, &doc) == nil {
				delete(doc, "generatedAt")
				raw, e = json.Marshal(doc)
			}
			if e == nil {
				_, e = h.Write(raw)
			}
		} else {
			_, e = io.Copy(h, f)
		}
		f.Close()
		if e == nil {
			count++
		}
	}
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// observeContent only reports changed files reachable through the recording index,
// including partial writes when a subsequent step fails.
func observeContent(s *Storage, id string, notify func(Entry, bool)) func(bool) {
	h := fnv.New32a()
	h.Write([]byte(s.TranscriptsDir() + id))
	mu := &contentLocks[h.Sum32()%64]
	mu.Lock()
	before := contentSnapshot(s, id)
	return func(success bool) {
		after := contentSnapshot(s, id)
		entry, ok := s.FindByID(id)
		mu.Unlock()
		if notify != nil && ok && after != "" && after != before {
			notify(entry, !success)
		}
	}
}

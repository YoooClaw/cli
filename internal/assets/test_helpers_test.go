package assets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func fixture(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	sources := map[string]Source{}
	for kind := range limits {
		sources[kind] = Source{Dir: filepath.Join(root, kind)}
	}
	s := New("test-profile", sources)
	s.now = func() time.Time { return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) }
	return s
}
func put(t *testing.T, s *Service, kind, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(s.sources[kind].Dir, name)
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}
func replace(t *testing.T, path string, raw []byte) {
	t.Helper()
	if e := fsutil.WriteAtomic(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func marshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

func decode(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
func notification(n int) map[string]any {
	return map[string]any{"appName": "app.example", "title": fmt.Sprint(n), "content": "消息😀", "timestamp": "2026-10-22T08:00:00+08:00", "clientLabel": "phone-a"}
}
func notifications(t *testing.T, n int) []byte {
	t.Helper()
	var out []byte
	for i := 0; i < n; i++ {
		out = append(out, marshal(t, notification(i))...)
		out = append(out, '\n')
	}
	return out
}
func get(t *testing.T, s *Service, kind string, q url.Values) Response {
	t.Helper()
	res, e := s.Read(context.Background(), kind, q)
	if e != nil {
		t.Fatal(e)
	}
	checkPage(t, res)
	return res
}
func checkPage(t *testing.T, p Response) {
	t.Helper()
	raw := marshal(t, p)
	if len(raw) > ResponseLimit {
		t.Fatalf("response exceeds limit: %d", len(raw))
	}
	if !p.OK || p.Items == nil || p.Warnings == nil {
		t.Fatalf("invalid envelope: %+v", p)
	}
	for _, it := range p.Items {
		maximum := ItemLimit
		if it.Data["schemaVersion"] != nil {
			maximum = HealthItemLimit
		}
		if len(marshal(t, it)) > maximum {
			t.Fatal("item exceeds cap")
		}
		if it.TruncatedFields == nil {
			t.Fatal("null truncatedFields")
		}
	}
}
func nextValues(p Response, n int) url.Values {
	q := url.Values{"limit": {fmt.Sprint(n)}}
	if p.NextCursor != nil {
		q.Set("cursor", *p.NextCursor)
	}
	return q
}
func warning(p Response, code string) int {
	for _, w := range p.Warnings {
		if w.Code == code {
			return w.Count
		}
	}
	return 0
}
func assertError(t *testing.T, s *Service, kind string, q url.Values, code string, status int) {
	t.Helper()
	_, e := s.Read(context.Background(), kind, q)
	if e == nil || e.Code != code || e.Status != status {
		t.Fatalf("want %d %s, got %+v", status, code, e)
	}
}
func collect(t *testing.T, s *Service, kind string, q url.Values) []Item {
	t.Helper()
	seen := map[string]bool{}
	var items []Item
	lastPosition := ""
	for page := 0; page < 200; page++ {
		p := get(t, s, kind, q)
		for _, it := range p.Items {
			if seen[it.RecordID] {
				t.Fatal("duplicate record", it.RecordID)
			}
			seen[it.RecordID] = true
			items = append(items, it)
		}
		if p.NextCursor == nil {
			return items
		}
		c, e := s.decode(*p.NextCursor)
		if e != nil {
			t.Fatal(e)
		}
		c.Exp = 0
		position := string(marshal(t, c))
		if position == lastPosition {
			t.Fatal("cursor did not advance")
		}
		lastPosition = position
		q.Set("cursor", *p.NextCursor)
	}
	t.Fatal("pagination failed to terminate")
	return nil
}

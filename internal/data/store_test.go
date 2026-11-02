package data

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/YoooClaw/cli/internal/config"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func put(t *testing.T, s *Store, b []byte, m Metadata, label string) PutResult {
	t.Helper()
	r, e := s.Put(bytes.NewReader(b), m, label)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestRoundTripDedupIdempotencyGC(t *testing.T) {
	s := New(t.TempDir(), config.DataSection{})
	if e := s.Init(); e != nil {
		t.Fatal(e)
	}
	raw := []byte{0, 255, 254, 1, 13, 10}
	a := put(t, s, raw, Metadata{Name: "../../x", IdempotencyKey: "retry"}, "local")
	b := put(t, s, raw, Metadata{}, "phone")
	retry := put(t, s, raw, Metadata{IdempotencyKey: "retry"}, "local")
	if a.Data.ID == b.Data.ID || !b.Deduped || retry.Data.ID != a.Data.ID || a.Data.Name != "x" || a.Data.Preview != nil {
		t.Fatal(a, b, retry)
	}
	if _, e := s.Put(strings.NewReader("other"), Metadata{IdempotencyKey: "retry"}, "local"); e == nil {
		t.Fatal("conflict accepted")
	}
	p, e := s.Path(a.Data)
	if e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(p)
	if e != nil || !bytes.Equal(got, raw) {
		t.Fatal("roundtrip", e)
	}
	u, e := s.Usage()
	if e != nil || u.Objects != 2 || u.Bytes != int64(len(raw)) {
		t.Fatal(u, e)
	}
	if e = s.Remove(a.Data.ID, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GC(false); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(p); e != nil {
		t.Fatal(e)
	}
	if e = s.Remove(b.Data.ID, ""); e != nil {
		t.Fatal(e)
	}
	g, e := s.GC(true)
	if e != nil || g.Files != 1 {
		t.Fatal(g, e)
	}
	s.GC(false)
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("orphan remains")
	}
	empty := put(t, s, nil, Metadata{}, "")
	if empty.Data.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal(empty)
	}
}
func TestLimitsConcurrencyAndIsolation(t *testing.T) {
	s := New(t.TempDir(), config.DataSection{MaxObjectBytes: 4, QuotaBytes: 4})
	put(t, s, []byte("1234"), Metadata{}, "local")
	if _, e := s.Put(strings.NewReader("12345"), Metadata{}, ""); e == nil {
		t.Fatal("size")
	}
	if _, e := s.Put(strings.NewReader("x"), Metadata{}, ""); e == nil {
		t.Fatal("quota")
	}
	put(t, s, []byte("1234"), Metadata{}, "phone")
	for _, scope := range []string{"phone", "default"} {
		p, e := s.List(Filter{Scope: scope})
		if e != nil {
			t.Fatal(e)
		}
		for _, o := range p.Items {
			if o.ClientLabel == "local" {
				t.Fatal("private object leaked")
			}
		}
	}
	s = New(t.TempDir(), config.DataSection{QuotaBytes: 10})
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			New(s.Dir, s.Config).Put(strings.NewReader(fmt.Sprintf("%02d", i)), Metadata{}, "local")
			s.GC(false)
		}(i)
	}
	wg.Wait()
	u, e := s.Usage()
	if e != nil || u.Bytes != 10 || u.Objects != 5 {
		t.Fatal(u, e)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "tmp"))
	if len(entries) != 0 {
		t.Fatal("temp leak")
	}
}
func TestMetadataFiltersPreviewAndTampering(t *testing.T) {
	s := New(t.TempDir(), config.DataSection{})
	for _, m := range []Metadata{{Meta: json.RawMessage(`[]`)}, {Meta: json.RawMessage(`null`)}, {Meta: json.RawMessage(`{oops`)}, {Title: strings.Repeat("x", 513)}, {Tags: []string{strings.Repeat("字", 65)}}, {Meta: json.RawMessage(`{"a":` + strings.Repeat("[", 20) + `0` + strings.Repeat("]", 20) + `}`)}} {
		if _, e := s.Put(strings.NewReader("x"), m, ""); e == nil {
			t.Fatal("invalid metadata", m)
		}
	}
	a := put(t, s, []byte("meeting notes"), Metadata{Name: "notes.md", Tags: []string{" work ", "work", "q3"}, Source: "agent"}, "a")
	put(t, s, []byte("second"), Metadata{Name: "two.md", Tags: []string{"work"}, Source: "agent"}, "a")
	if a.Data.Format != "markdown" || a.Data.Preview == nil || len(a.Data.Tags) != 2 {
		t.Fatal(a)
	}
	p, e := s.List(Filter{Type: "markdown", Source: "agent", Tags: []string{"work"}, Limit: 1})
	if e != nil || p.NextCursor == "" {
		t.Fatal(p, e)
	}
	q, e := s.List(Filter{Type: "markdown", Source: "agent", Tags: []string{"work"}, Limit: 1, Cursor: p.NextCursor})
	if e != nil || len(q.Items) != 1 || q.Items[0].ID == p.Items[0].ID {
		t.Fatal(q, e)
	}
	if _, e = s.List(Filter{Cursor: p.NextCursor, Scope: "b"}); e == nil {
		t.Fatal("cursor scope")
	}
	p, e = s.List(Filter{Query: "meeting", From: time.Now().Add(-time.Hour).Format(time.RFC3339), To: time.Now().Add(time.Hour).Format(time.RFC3339)})
	if e != nil || len(p.Items) != 1 {
		t.Fatal(p, e)
	}
	invalid := put(t, s, append(bytes.Repeat([]byte("a"), 4096), 255), Metadata{}, "")
	if invalid.Data.Preview != nil {
		t.Fatal("invalid utf8 preview")
	}
	x, _ := s.read()
	x.Objects[0].BlobPath = "../../secret"
	s.save(x)
	if e = s.Check(); e == nil {
		t.Fatal("tampering accepted")
	}
}
func TestRetentionAndIndexFailure(t *testing.T) {
	days := 1
	s := New(t.TempDir(), config.DataSection{RetentionDays: &days})
	a := put(t, s, []byte("old"), Metadata{}, "")
	x, _ := s.read()
	x.Objects[0].CreatedAt = time.Now().Add(-48 * time.Hour)
	s.save(x)
	b := put(t, s, []byte("new"), Metadata{}, "")
	if _, e := s.Show(a.Data.ID, ""); e == nil {
		t.Fatal("retention")
	}
	if _, e := s.Path(b.Data); e != nil {
		t.Fatal("retention removed in-flight upload", e)
	}
	if e := os.Remove(filepath.Join(s.Dir, "index.json")); e != nil {
		t.Fatal(e)
	}
	os.Mkdir(filepath.Join(s.Dir, "index.json"), 0700)
	if _, e := s.Put(strings.NewReader("fail"), Metadata{}, ""); e == nil {
		t.Fatal("index failure accepted")
	}
}
func TestSymlinkEscape(t *testing.T) {
	s := New(t.TempDir(), config.DataSection{})
	a := put(t, s, []byte("inside"), Metadata{}, "")
	p, _ := s.Path(a.Data)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Remove(p)
	if e := os.Symlink(outside, p); e != nil {
		t.Skip(e)
	}
	if _, e := s.Path(a.Data); e == nil {
		t.Fatal("symlink escaped")
	}
	if _, e := s.Put(strings.NewReader("inside"), Metadata{}, ""); e == nil {
		t.Fatal("symlink reused")
	}
}

type interruptedReader struct{ sent bool }

func (r *interruptedReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, []byte("partial")), nil
	}
	return 0, fmt.Errorf("connection lost")
}
func TestInterruptedWriteAndPrivatePermissions(t *testing.T) {
	s := New(t.TempDir(), config.DataSection{})
	a := put(t, s, []byte("keep"), Metadata{}, "")
	if _, e := s.Put(&interruptedReader{}, Metadata{}, ""); e == nil {
		t.Fatal("interrupted write succeeded")
	}
	x, e := s.read()
	if e != nil || len(x.Objects) != 1 || x.Objects[0].ID != a.Data.ID {
		t.Fatal(x, e)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "tmp"))
	if len(entries) != 0 {
		t.Fatal("partial temp file")
	}
	if e = writeIndex(filepath.Join(s.Dir, "blobs"), []byte("bad")); e == nil {
		t.Fatal("index rename to directory succeeded")
	}
	if e = s.Init(); e != nil {
		t.Fatal(e)
	}
}

func TestDisplayNameRootAndPreviewRuneBoundary(t *testing.T) {
	s := New(t.TempDir(), config.DataSection{})
	for _, name := range []string{"/", `\`, "../..", "dir/../file\n.txt"} {
		a := put(t, s, []byte(strings.Repeat("字", 1000)), Metadata{Name: name}, "")
		if strings.ContainsAny(a.Data.Name, "/\\\n") || strings.Contains(a.Data.Name, "..") {
			t.Fatal(a.Data.Name)
		}
		if a.Data.Preview == nil || len(*a.Data.Preview) > 2048 {
			t.Fatal("preview bound")
		}
	}
}

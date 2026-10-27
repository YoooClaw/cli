package assets

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/webpage"
)

func webRow(id, at, label string) map[string]any {
	return map[string]any{"urlHash": id, "url": "https://example.com/" + id, "canonicalUrl": "https://example.com/" + id,
		"title": "An article", "capturedAt": at, "firstCapturedAt": at, "captureCount": 1, "relativePath": "files/" + id + ".md", "clientLabel": label}
}
func webIndex(t *testing.T, s *Service, rows ...any) string {
	return put(t, s, "web-pages", "index.json", marshal(t, map[string]any{"pages": rows}))
}

func TestWebSortAllSourcesCurrentEntriesAndIndexRevision(t *testing.T) {
	s := fixture(t)
	rows := []any{
		webRow("a", "2026-10-22T09:00:00+08:00", "phone-a"),
		webRow("b", "2026-10-22T02:00:00Z", "phone-a"),
		webRow("c", "2026-10-22T10:00:00+08:00", "legacy"),
		webRow("other-source", "2026-10-23T00:00:00Z", "phone-b"),
		webRow("d", "bad", "default"), webRow("e", "", ""),
	}
	path := webIndex(t, s, rows...)
	items := collect(t, s, "web-pages", url.Values{"limit": {"1"}})
	if len(items) != 6 {
		t.Fatal("source label filtered web entries")
	}
	for i, id := range []string{"other-source", "c", "b", "a", "e", "d"} {
		want := map[string]any{"title": "An article", "url": "https://example.com/" + id}
		if !reflect.DeepEqual(items[i].Data, want) || items[i].StorageDate != nil ||
			items[i].RecordID != digest(s.profile, "web-pages", id) {
			t.Fatalf("web row %+v", items[i])
		}
	}
	p := get(t, s, "web-pages", url.Values{"limit": {"1"}})
	rows = append(rows, webRow("new", "2026-10-23T00:00:00Z", "phone-a"))
	replace(t, path, marshal(t, map[string]any{"pages": rows}))
	assertError(t, s, "web-pages", nextValues(p, 1), "CURSOR_EXPIRED", 410)
}

func TestWebPageDefaultMaximumAndByteLimit(t *testing.T) {
	s := fixture(t)
	rows := make([]any, 0, 60)
	for i := 0; i < 60; i++ {
		m := webRow(fmt.Sprintf("%064d", i), "2026-10-22T00:00:00Z", "")
		m["url"] = "https://example.com/" + strings.Repeat("u", 40000)
		rows = append(rows, m)
	}
	webIndex(t, s, rows...)
	p := get(t, s, "web-pages", url.Values{"limit": {"50"}})
	if len(p.Items) == 0 || len(p.Items) >= 50 || p.NextCursor == nil || len(p.Warnings) != 0 {
		t.Fatal("web byte limit or unexpected body warning")
	}
	if len(collect(t, s, "web-pages", url.Values{"limit": {"50"}})) != 60 {
		t.Fatal("web byte cap skipped entry")
	}
	s = fixture(t)
	rows = nil
	for i := 0; i < 60; i++ {
		rows = append(rows, webRow(fmt.Sprint(i), "2026-10-22T00:00:00Z", ""))
	}
	webIndex(t, s, rows...)
	if len(get(t, s, "web-pages", url.Values{}).Items) != 20 || len(get(t, s, "web-pages", url.Values{"limit": {"50"}}).Items) != 50 {
		t.Fatal("web count cap")
	}
}

func TestWebIndexOnlyURLFallbackAndUnusedMetadata(t *testing.T) {
	for _, tt := range []struct {
		name      string
		original  any
		canonical any
		want      string
	}{
		{"original", "https://example.com/a?source=clip#part", "https://example.com/a", "https://example.com/a?source=clip#part"},
		{"missing", nil, "https://example.com/a", "https://example.com/a"},
		{"empty", "", "https://example.com/a", "https://example.com/a"},
		{"blank", "  ", "https://example.com/a", "https://example.com/a"},
		{"wrong type", 42, "https://example.com/a", "https://example.com/a"},
		{"original only", "https://example.com/a", nil, "https://example.com/a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture(t)
			// Neither body paths nor capture/version metadata are required for a list entry.
			row := map[string]any{"urlHash": "a", "title": "中文😀", "url": tt.original,
				"canonicalUrl": tt.canonical, "captureCount": "unused", "versionCount": "unused"}
			webIndex(t, s, row)
			p := get(t, s, "web-pages", url.Values{})
			if len(p.Items) != 1 || len(p.Warnings) != 0 ||
				!reflect.DeepEqual(p.Items[0].Data, map[string]any{"title": "中文😀", "url": tt.want}) {
				t.Fatalf("index projection: %+v", p)
			}
		})
	}
}

func TestWebBodyPathsIgnoredAndBodyChangesPreserveCursor(t *testing.T) {
	s := fixture(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	body := put(t, s, "web-pages", "files/body.md", []byte("private body"))
	paths := []string{"files/missing.md", "files", "../../secret.md", outside, "files/body.md"}
	rows := make([]any, 0, len(paths))
	for i, path := range paths {
		row := webRow(fmt.Sprint(i), "2026-10-22T00:00:00Z", "")
		row["relativePath"] = path
		rows = append(rows, row)
	}
	webIndex(t, s, rows...)
	first := get(t, s, "web-pages", url.Values{"limit": {"1"}})
	if err := os.Remove(body); err != nil {
		t.Fatal(err)
	}
	// Only the index revision binds the cursor; body removal must not expire it.
	rest := get(t, s, "web-pages", nextValues(first, 50))
	if len(first.Warnings)+len(rest.Warnings) != 0 || len(rest.Items) != len(paths)-1 || rest.NextCursor != nil {
		t.Fatalf("body affected pagination: %+v", rest)
	}
	for _, it := range append(first.Items, rest.Items...) {
		if len(it.Data) != 2 || it.Data["title"] != "An article" || it.Status != "ok" {
			t.Fatalf("body affected entry: %+v", it)
		}
	}
}

func TestWebTitleBoundAndOversizedURL(t *testing.T) {
	s := fixture(t)
	longTitle := webRow("b", "2026-10-22T00:00:00Z", "")
	longTitle["title"] = strings.Repeat("字😀", 400)
	longURL := webRow("a", "2026-10-22T00:00:00Z", "")
	longURL["url"] = "https://example.com/" + strings.Repeat("u", ItemLimit)
	webIndex(t, s, longTitle, longURL, webRow("0", "2026-10-22T00:00:00Z", ""))
	items := collect(t, s, "web-pages", url.Values{"limit": {"1"}})
	if len(items) != 3 || items[0].Data["title"] != strings.Repeat("字😀", 256) ||
		!reflect.DeepEqual(items[0].TruncatedFields, []string{"title"}) || items[0].Data["url"] != longTitle["url"] {
		t.Fatalf("title cap: %+v", items)
	}
	if items[1].Status != "oversized" || items[1].Data != nil || items[2].Status != "ok" {
		t.Fatal("oversized URL was truncated, skipped or blocked continuation")
	}
}

func TestWebCorruptAndOversizedIndex(t *testing.T) {
	for _, raw := range [][]byte{[]byte("{"), []byte(`{"pages":null}`), []byte(`{"pages":[null]}`),
		[]byte(`{"pages":[{"urlHash":"a","url":" "}]}`),
		[]byte(`{"pages":[{"url":"https://example.com"}]}`)} {
		s := fixture(t)
		put(t, s, "web-pages", "index.json", raw)
		assertError(t, s, "web-pages", url.Values{}, "SOURCE_READ_FAILED", 500)
	}
	s := fixture(t)
	path := put(t, s, "web-pages", "index.json", nil)
	if e := os.Truncate(path, IndexLimit+1); e != nil {
		t.Fatal(e)
	}
	assertError(t, s, "web-pages", url.Values{}, "SOURCE_TOO_LARGE", 503)
}

func TestWebReaderCoordinatesExistingWriter(t *testing.T) {
	s := fixture(t)
	src := s.sources["web-pages"]
	src.WithReadView = webpage.WithReadView
	s.sources["web-pages"] = src
	_, err := webpage.Ingest(src.Dir, webpage.Payload{URL: "https://example.com/test?from=clip#part", CanonicalURL: "https://example.com/test", Title: "T",
		CapturedAt: "2026-10-22T00:00:00Z", Markdown: "# T\n\nbody"}, "phone-a", quietLogger{})
	if err != nil {
		t.Fatal(err)
	}
	p := get(t, s, "web-pages", url.Values{})
	if len(p.Items) != 1 || !reflect.DeepEqual(p.Items[0].Data, map[string]any{"title": "T", "url": "https://example.com/test?from=clip#part"}) {
		t.Fatalf("real web store: %+v", p)
	}
}

type quietLogger struct{}

func (quietLogger) Info(string)  {}
func (quietLogger) Warn(string)  {}
func (quietLogger) Error(string) {}

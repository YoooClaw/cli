package image

import (
	"path/filepath"
	"testing"
)

func TestResolveFile(t *testing.T) {
	t.Parallel()
	if got := ResolveFile("/imgs", "a/b.png"); got != filepath.Join("/imgs", "a/b.png") {
		t.Errorf("relative resolve = %q", got)
	}
	abs := filepath.Join(t.TempDir(), "x.png")
	if got := ResolveFile("/imgs", abs); got != abs {
		t.Errorf("absolute path should be returned as-is: %q", got)
	}
}

func TestSortByCreatedDesc(t *testing.T) {
	t.Parallel()
	entries := []Entry{
		{ImageID: "a", Metadata: Metadata{CreatedAt: "2026-06-01T00:00:00Z"}},
		{ImageID: "b", Metadata: Metadata{CreatedAt: "2026-06-03T00:00:00Z"}},
		{ImageID: "c", Metadata: Metadata{CreatedAt: "2026-06-02T00:00:00Z"}},
	}
	SortByCreatedDesc(entries)
	if entries[0].ImageID != "b" || entries[1].ImageID != "c" || entries[2].ImageID != "a" {
		t.Errorf("sort order wrong: %v", []string{entries[0].ImageID, entries[1].ImageID, entries[2].ImageID})
	}
}

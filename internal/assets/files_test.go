package assets

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestManyDateFilesOnlyVisitEightEmptySources(t *testing.T) {
	s := fixture(t)
	newest := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 600; i++ {
		put(t, s, "notifications", newest.AddDate(0, 0, -i).Format(time.DateOnly)+".jsonl", nil)
	}
	for _, name := range []string{"state.json", "2026-02-30.jsonl", "2026-1-01.jsonl", "readme.md"} {
		put(t, s, "notifications", name, []byte("not an asset"))
	}
	src := s.sources["notifications"]
	views := 0
	src.WithReadView = func(fn func() error) error { views++; return fn() }
	s.sources["notifications"] = src
	p := get(t, s, "notifications", url.Values{})
	if len(p.Items) != 0 || p.NextCursor == nil || views > 17 {
		t.Fatalf("visited too many source files: %d", views)
	}
	c, e := s.decode(*p.NextCursor)
	if e != nil || c.FileKey != newest.AddDate(0, 0, -8).Format(time.DateOnly) {
		t.Fatalf("incorrect next file %+v %v", c, e)
	}
	// A day not yet visited can accept late uploads. Already captured files
	// keep their snapshot; later files take a fresh snapshot on first entry.
	date := newest.AddDate(0, 0, -9).Format(time.DateOnly)
	put(t, s, "notifications", date+".jsonl", notifications(t, 2))
	next := get(t, s, "notifications", nextValues(p, 50))
	if len(next.Items) != 2 || *next.Items[0].StorageDate != date {
		t.Fatal("missed late upload to unvisited file")
	}
}

func TestBoundarySizedLinesAndShortReadFail(t *testing.T) {
	for _, n := range []int{LineLimit - 1, LineLimit, LineLimit + 1} {
		for _, forward := range []bool{false, true} {
			s := fixture(t)
			path := put(t, s, "locations", "2026-10-22.jsonl", []byte(strings.Repeat("x", n)+"\n"))
			f, e := os.Open(path)
			if e != nil {
				t.Fatal(e)
			}
			budget := ScanLimit
			reader := lineReader{ctx: context.Background(), f: f, end: int64(n + 1), remaining: &budget}
			c := cursor{SnapshotEnd: int64(n + 1), BeforeByte: int64(n + 1), Query: map[string]string{}}
			if forward {
				c.Query["direction"] = "forward"
			}
			row, _, e := reader.next(c)
			f.Close()
			if e != nil || row.oversized != (n > LineLimit) || len(row.raw) > LineLimit || budget < 0 {
				t.Fatalf("line %d forward=%v: %+v", n, forward, e)
			}
		}
	}
	s := fixture(t)
	path := put(t, s, "locations", "2026-10-22.jsonl", []byte("x\n"))
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	budget := ScanLimit
	reader := lineReader{ctx: context.Background(), f: f, end: 1000, remaining: &budget}
	if _, _, e := reader.next(cursor{SnapshotEnd: 1000, BeforeByte: 1000}); e == nil {
		t.Fatal("short read silently treated as EOF")
	}
}

func TestHealthMetricCountAndDuplicateValidation(t *testing.T) {
	for _, n := range []int{256, 257} {
		s := fixture(t)
		raw := []byte(`{"schemaVersion":3,"date":"2026-10-22","provider":"healthkit","timeZone":"UTC","updatedAt":"2026-10-22T00:00:00Z"}` + "\n")
		for i := 0; i < n; i++ {
			raw = append(raw, []byte(fmt.Sprintf(`{"metric":"metric_%d","name":"Name","value":%d,"unit":"count"}`+"\n", i, i))...)
		}
		put(t, s, "health-data", "2026-10-22.jsonl", raw)
		if n == 257 {
			assertError(t, s, "health-data", url.Values{}, "SOURCE_READ_FAILED", 500)
		} else {
			p := get(t, s, "health-data", url.Values{})
			if len(p.Items) != 1 {
				t.Fatal("complete 256-metric day not returned")
			}
		}
	}
	s := fixture(t)
	raw := []byte(`{"schemaVersion":3,"date":"2026-10-22","provider":"healthkit","timeZone":"UTC","updatedAt":"2026-10-22T00:00:00Z"}` + "\n")
	metric := []byte(`{"metric":"steps","name":"Steps","value":1,"unit":"count"}` + "\n")
	raw = append(append(raw, metric...), metric...)
	put(t, s, "health-data", "2026-10-22.jsonl", raw)
	assertError(t, s, "health-data", url.Values{}, "SOURCE_READ_FAILED", 500)
}

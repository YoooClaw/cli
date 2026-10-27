package assets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"reflect"
	"testing"
)

func TestEOFRecordIDSurvivesAppendAndCalendarDirection(t *testing.T) {
	s := fixture(t)
	path := put(t, s, "notifications", "2026-10-22.jsonl", bytes.TrimSuffix(notifications(t, 3), []byte{'\n'}))
	first := get(t, s, "notifications", url.Values{}).Items
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write(append(append([]byte{'\n'}, marshal(t, notification(3))...), '\n'))
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	next := get(t, s, "notifications", url.Values{}).Items
	if next[1].RecordID != first[0].RecordID {
		t.Fatal("completing EOF separator changed identity")
	}
	put(t, s, "calendars", "2026-10-22.jsonl", bytes.TrimSuffix(calendarRows(t, 3), []byte{'\n'}))
	forward := get(t, s, "calendars", url.Values{"anchorDate": {"2026-10-22"}})
	backward := get(t, s, "calendars", url.Values{"anchorDate": {"2026-10-22"}, "direction": {"backward"}})
	for i := range forward.Items {
		if forward.Items[i].RecordID != backward.Items[2-i].RecordID {
			t.Fatal("direction changed occurrence identity")
		}
	}
}

func TestLineReaderReferenceOrderBoundariesAndBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(25))
	// Delimiters at, before and after 64 KiB boundaries, and several EOF shapes.
	for trial := 0; trial < 30; trial++ {
		var raw []byte
		var want [][]byte
		for i := 0; i < 12; i++ {
			n := rng.Intn(2 * BlockSize)
			if i == 0 {
				n = BlockSize + trial%3 - 1
			}
			line := bytes.Repeat([]byte{byte('a' + i)}, n)
			want = append(want, line)
			raw = append(raw, line...)
			if i != 11 || trial%2 == 0 {
				if trial%3 == 0 {
					raw = append(raw, '\r')
				}
				raw = append(raw, '\n')
			}
		}
		s := fixture(t)
		path := put(t, s, "locations", "2026-10-22.jsonl", raw)
		for _, forward := range []bool{true, false} {
			f, e := os.Open(path)
			if e != nil {
				t.Fatal(e)
			}
			budget := ScanLimit
			reader := lineReader{ctx: context.Background(), f: f, end: int64(len(raw)), remaining: &budget}
			c := cursor{SnapshotEnd: int64(len(raw)), BeforeByte: int64(len(raw)), Query: map[string]string{}}
			if forward {
				c.Query["direction"] = "forward"
			}
			for i := 0; i < len(want); i++ {
				row, next, e := reader.next(c)
				if e != nil {
					t.Fatal(e)
				}
				index := i
				if !forward {
					index = len(want) - 1 - i
				}
				if !bytes.Equal(row.raw, want[index]) {
					t.Fatalf("trial=%d direction=%v row=%d length=%d want=%d", trial, forward, index, len(row.raw), len(want[index]))
				}
				if next.position() == c.position() || len(row.raw) > LineLimit || budget < 0 {
					t.Fatal("reader failed progress/bounds")
				}
				c = next
			}
			if forward && c.position() != int64(len(raw)) || !forward && c.position() != 0 {
				t.Fatal("wrong final byte boundary")
			}
			f.Close()
		}
	}
}

func TestInvalidUTF8MalformedFieldsAndReadFailures(t *testing.T) {
	s := fixture(t)
	raw := append(notifications(t, 1), []byte("\xff\n{\"appName\":false}\n")...)
	put(t, s, "notifications", "2026-10-22.jsonl", raw)
	p := get(t, s, "notifications", url.Values{})
	if len(p.Items) != 1 || warning(p, "INVALID_RECORD_SKIPPED") != 2 {
		t.Fatal("invalid record handling")
	}
	otherSource := notification(1)
	otherSource["timestamp"] = "bad"
	otherSource["clientLabel"] = "phone-b"
	put(t, s, "notifications", "2026-10-22.jsonl", append(marshal(t, otherSource), '\n'))
	p = get(t, s, "notifications", url.Values{})
	if len(p.Items) != 0 || warning(p, "INVALID_RECORD_SKIPPED") != 1 {
		t.Fatal("source label suppressed invalid-record warning")
	}
	for _, kind := range []string{"notifications", "health-data", "web-pages"} {
		s := fixture(t)
		if kind != "web-pages" {
			put(t, s, kind, "2026-10-22.jsonl", notifications(t, 1))
		}
		src := s.sources[kind]
		src.WithReadView = func(func() error) error { return errors.New("read view failure") }
		s.sources[kind] = src
		assertError(t, s, kind, url.Values{}, "SOURCE_READ_FAILED", 500)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Read(ctx, "notifications", url.Values{}); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestProjectionFurtherShrinkingDoesNotRewriteIdentity(t *testing.T) {
	m := notification(0)
	m["content"] = string(bytes.Repeat([]byte{0}, 8192))
	m["clientLabel"] = string(bytes.Repeat([]byte{0}, 1024))
	for _, k := range []string{"title", "appName", "appDisplayName", "senderName", "conversationName"} {
		m[k] = string(bytes.Repeat([]byte{0}, 512))
	}
	it, ok := project("notifications", m, "stable-id", "2026-10-22")
	if !ok || len(marshal(t, it)) > ItemLimit || it.RecordID != "stable-id" || len(it.TruncatedFields) == 0 {
		t.Fatal("JSON-escaping size cap not applied")
	}
	if m["content"] == it.Data["content"] {
		t.Fatal("expected further text reduction")
	}
}

func TestIdentityRegistryEvictionExpiresAndIsBounded(t *testing.T) {
	s := fixture(t)
	path := put(t, s, "notifications", "2026-10-22.jsonl", notifications(t, 3))
	p := get(t, s, "notifications", url.Values{"limit": {"1"}})
	info, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4096; i++ {
		s.fileID(fmt.Sprint(i), info)
	}
	if len(s.files) != 4096 {
		t.Fatal("unbounded identity registry")
	}
	assertError(t, s, "notifications", nextValues(p, 1), "CURSOR_EXPIRED", 410)
}

func TestInvalidRecordScanBudgetHasEmptyProgressPage(t *testing.T) {
	s := fixture(t)
	validRow := notification(0)
	invalid := notification(1)
	invalid["timestamp"] = "bad"
	invalid["clientLabel"] = "phone-b"
	invalid["content"] = string(bytes.Repeat([]byte{'x'}, 10000))
	raw := append(marshal(t, validRow), '\n')
	for i := 0; i < 600; i++ {
		raw = append(raw, marshal(t, invalid)...)
		raw = append(raw, '\n')
	}
	put(t, s, "notifications", "2026-10-22.jsonl", raw)
	p := get(t, s, "notifications", url.Values{})
	if len(p.Items) != 0 || p.NextCursor == nil || warning(p, "SCAN_BUDGET_REACHED") != 1 || warning(p, "INVALID_RECORD_SKIPPED") == 0 {
		t.Fatal("invalid records bypassed scan budget")
	}
	items := collect(t, s, "notifications", nextValues(p, 1))
	if len(items) != 1 || !reflect.DeepEqual(items[0].Data["title"], "0") {
		t.Fatal("invalid-record scan lost valid row")
	}
}

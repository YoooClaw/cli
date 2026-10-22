package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestThousandNotificationsAndCrossFileCursor(t *testing.T) {
	s := fixture(t)
	put(t, s, "notifications", "2026-10-22.jsonl", notifications(t, 1000))
	items := collect(t, s, "notifications", url.Values{"limit": {"100"}})
	if len(items) != 1000 {
		t.Fatalf("got %d", len(items))
	}
	for i, it := range items {
		if it.Data["title"] != fmt.Sprint(999-i) {
			t.Fatalf("physical order at %d: %+v", i, it)
		}
	}
	s = fixture(t)
	put(t, s, "notifications", "2026-10-22.jsonl", notifications(t, 123))
	put(t, s, "notifications", "2026-10-21.jsonl", notifications(t, 8))
	q := url.Values{"limit": {"50"}}
	for i, n := range []int{50, 50, 23, 8} {
		p := get(t, s, "notifications", q)
		if len(p.Items) != n {
			t.Fatalf("page %d has %d, want %d", i, len(p.Items), n)
		}
		if i < 3 && p.NextCursor == nil || i == 3 && p.NextCursor != nil {
			t.Fatal("wrong termination")
		}
		if i == 2 {
			c, _ := s.decode(*p.NextCursor)
			if c.FileKey != "2026-10-21" || c.BeforeByte != c.SnapshotEnd {
				t.Fatalf("wrong cross-file cursor: %+v", c)
			}
		}
		q = nextValues(p, 50)
	}
}

func TestResponseByteCapDoesNotConsumeDeferredRecord(t *testing.T) {
	s := fixture(t)
	var lines []byte
	for i := 0; i < 40; i++ {
		n := notification(i)
		n["content"] = strings.Repeat("\x00", 8192)
		lines = append(lines, marshal(t, n)...)
		lines = append(lines, '\n')
	}
	path := put(t, s, "notifications", "2026-10-22.jsonl", lines)
	first := get(t, s, "notifications", url.Values{"limit": {"100"}})
	if len(first.Items) == 0 || len(first.Items) >= 100 || first.NextCursor == nil {
		t.Fatal("byte cap not exercised")
	}
	items := collect(t, s, "notifications", url.Values{"limit": {"100"}})
	if len(items) != 40 {
		t.Fatalf("byte cap lost rows: %d", len(items))
	}
	for i, it := range items {
		if it.Data["title"] != fmt.Sprint(39-i) {
			t.Fatal("deferred row skipped")
		}
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, lines) {
		t.Fatal("read mutated source")
	}
}

func TestAppendSnapshotRetryAndRefresh(t *testing.T) {
	s := fixture(t)
	raw := notifications(t, 4)
	path := put(t, s, "notifications", "2026-10-22.jsonl", raw)
	first := get(t, s, "notifications", url.Values{"limit": {"1"}})
	q := nextValues(first, 1)
	a := get(t, s, "notifications", q)
	b := get(t, s, "notifications", q)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same cursor retry changed response")
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write(append(marshal(t, notification(4)), '\n'))
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	old := collect(t, s, "notifications", q)
	if len(old) != 3 || old[0].Data["title"] != "2" {
		t.Fatal("append leaked into snapshot")
	}
	fresh := get(t, s, "notifications", url.Values{"limit": {"1"}})
	if fresh.Items[0].Data["title"] != "4" {
		t.Fatal("refresh missed new tail")
	}
	if get(t, s, "notifications", nextValues(first, 3)).Items[0].RecordID != a.Items[0].RecordID {
		t.Fatal("append changed record ID")
	}
}

func TestReplacementTruncationDeletionAndNextFileDeletion(t *testing.T) {
	for _, mutation := range []string{"replace", "truncate", "delete", "next-file-delete"} {
		t.Run(mutation, func(t *testing.T) {
			s := fixture(t)
			raw := notifications(t, 3)
			path := put(t, s, "notifications", "2026-10-22.jsonl", raw)
			n := 1
			if mutation == "next-file-delete" {
				path = put(t, s, "notifications", "2026-10-21.jsonl", raw)
				n = 3
			}
			first := get(t, s, "notifications", url.Values{"limit": {fmt.Sprint(n)}})
			var e error
			switch mutation {
			case "replace":
				replace(t, path, raw)
			case "truncate":
				e = os.Truncate(path, 1)
			default:
				e = os.Remove(path)
			}
			if e != nil {
				t.Fatal(e)
			}
			assertError(t, s, "notifications", nextValues(first, 1), "CURSOR_EXPIRED", 410)
		})
	}
}

func TestUnicodeCRLFBlocksAndUnterminatedTail(t *testing.T) {
	s := fixture(t)
	a := notification(1)
	a["content"] = strings.Repeat("汉😀", 20000)
	raw := append(marshal(t, notification(0)), '\r', '\n')
	raw = append(raw, marshal(t, a)...)
	raw = append(raw, '\r', '\n')
	raw = append(raw, marshal(t, notification(2))...)
	path := put(t, s, "notifications", "2026-10-22.jsonl", raw)
	p := get(t, s, "notifications", url.Values{})
	if len(p.Items) != 3 || p.Items[0].Data["title"] != "2" {
		t.Fatal("CRLF / EOF rows lost")
	}
	text := p.Items[1].Data["content"].(string)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) != 8192 || len(p.Items[1].TruncatedFields) != 1 {
		t.Fatal("unsafe truncation")
	}
	torn := append(append([]byte(nil), raw...), []byte("\n{\"appName\":")...)
	if e := os.WriteFile(path, torn, 0600); e != nil {
		t.Fatal(e)
	}
	p = get(t, s, "notifications", url.Values{})
	if len(p.Items) != 3 || warning(p, "INCOMPLETE_TAIL_SKIPPED") != 1 {
		t.Fatalf("torn tail: %+v", p.Warnings)
	}
	complete := append(append(append([]byte(nil), raw...), '\n'), marshal(t, notification(3))...)
	if e := os.WriteFile(path, complete, 0600); e != nil {
		t.Fatal(e)
	}
	if len(get(t, s, "notifications", url.Values{}).Items) != 4 {
		t.Fatal("refresh did not see repaired tail")
	}
}

func TestHugeLineResumesWithProfilePlaceholder(t *testing.T) {
	for _, label := range []string{"", "phone-a", "phone-b"} {
		t.Run("source="+label, func(t *testing.T) {
			s := fixture(t)
			row := notification(1)
			row["clientLabel"] = label
			row["content"] = strings.Repeat("x", 10<<20)
			raw := append(notifications(t, 1), marshal(t, row)...)
			raw = append(raw, '\n')
			put(t, s, "notifications", "2026-10-22.jsonl", raw)
			p := get(t, s, "notifications", url.Values{})
			if len(p.Items) != 0 || p.NextCursor == nil || warning(p, "SCAN_BUDGET_REACHED") != 1 {
				t.Fatal("large line was not bounded")
			}
			c, _ := s.decode(*p.NextCursor)
			if c.SkipLine == nil || c.BeforeByte == c.SnapshotEnd || c.SnapshotEnd-c.BeforeByte > ScanLimit {
				t.Fatalf("bad skip state %+v", c)
			}
			items := collect(t, s, "notifications", nextValues(p, 50))
			if len(items) != 2 || items[1].Data["title"] != "0" {
				t.Fatalf("oversized row or following row lost: %+v", items)
			}
			if items[0].Status != "oversized" || items[0].Data != nil || items[0].SourceBytes < 10<<20 {
				t.Fatal("missing oversized placeholder")
			}
		})
	}
}

func TestEmptyBadFilesAndAllNotificationSources(t *testing.T) {
	s := fixture(t)
	for day := 1; day <= 10; day++ {
		put(t, s, "notifications", fmt.Sprintf("2026-10-%02d.jsonl", day), []byte("\ninvalid\n"))
	}
	p := get(t, s, "notifications", url.Values{})
	if len(p.Items) != 0 || p.NextCursor == nil || warning(p, "INVALID_RECORD_SKIPPED") != 8 {
		t.Fatalf("bad-file scan did not respect the date budget: %+v", p)
	}
	if len(collect(t, s, "notifications", nextValues(p, 50))) != 0 {
		t.Fatal("bad rows returned")
	}
	var raw []byte
	var rows []any
	labels := []string{"phone-a", "phone-b", "", "default", "legacy"}
	for i, label := range labels {
		n := notification(i)
		n["clientLabel"] = label
		rows = append(rows, n)
		raw = append(raw, marshal(t, n)...)
		raw = append(raw, '\n')
	}
	for _, format := range []string{"jsonl", "json"} {
		t.Run(format, func(t *testing.T) {
			s := fixture(t)
			body := raw
			if format == "json" {
				body = marshal(t, rows)
			}
			put(t, s, "notifications", "2026-10-22."+format, body)
			items := collect(t, s, "notifications", url.Values{"limit": {"1"}})
			if len(items) != len(labels) {
				t.Fatalf("source label filtered records: %+v", items)
			}
			for i, it := range items {
				index := len(labels) - 1 - i
				var label any
				if labels[index] != "" {
					label = labels[index]
				}
				if it.Data["title"] != fmt.Sprint(index) || it.Data["clientLabel"] != label {
					t.Fatalf("source metadata or physical order changed: %+v", it)
				}
			}
		})
	}
}

func TestLegacyArrayPrecedenceVersionsAndBudget(t *testing.T) {
	s := fixture(t)
	rows := []any{notification(0), notification(1), notification(2)}
	path := put(t, s, "notifications", "2026-10-22.json", marshal(t, rows))
	items := collect(t, s, "notifications", url.Values{"limit": {"1"}})
	if len(items) != 3 || items[0].Data["title"] != "2" {
		t.Fatal("legacy paging")
	}
	p := get(t, s, "notifications", url.Values{"limit": {"1"}})
	replace(t, path, marshal(t, rows))
	assertError(t, s, "notifications", nextValues(p, 1), "CURSOR_EXPIRED", 410)
	put(t, s, "notifications", "2026-10-22.jsonl", notifications(t, 1))
	if len(collect(t, s, "notifications", url.Values{"limit": {"1"}})) != 1 {
		t.Fatal("JSONL did not take precedence")
	}
	s = fixture(t)
	put(t, s, "notifications", "2026-10-22.json", bytes.Repeat([]byte(" "), ScanLimit+1))
	assertError(t, s, "notifications", url.Values{}, "SOURCE_TOO_LARGE", 503)
}

func TestSourceErrorsAndOversizedProjection(t *testing.T) {
	s := fixture(t)
	if e := os.MkdirAll(s.sources["locations"].Dir+"/2026-10-22.jsonl", 0700); e != nil {
		t.Fatal(e)
	}
	assertError(t, s, "locations", url.Values{}, "SOURCE_READ_FAILED", 500)
	s = fixture(t)
	obj := map[string]any{"longitude": 0, "latitude": 0, "id": strings.Repeat("x", ItemLimit+1)}
	put(t, s, "locations", "2026-10-22.jsonl", marshal(t, obj))
	p := get(t, s, "locations", url.Values{})
	if len(p.Items) != 1 || p.Items[0].Status != "oversized" {
		t.Fatal("identifier was truncated or item exceeded cap")
	}
	obj = map[string]any{"longitude": json.Number("0"), "latitude": json.Number("90"), "timestamp": false, "deviceType": 12}
	it, ok := project("locations", obj, "id", "2026-10-22")
	if !ok || it.Data["timestamp"] != nil || it.Data["deviceType"] != nil || it.Data["accuracyMeters"] != nil {
		t.Fatal("fabricated optional field")
	}
}

package assets

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func calendarDate(offset int) string {
	return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset).Format("2006-01-02")
}

func calendarQuery(direction string, days int) url.Values {
	anchor := calendarDate(0)
	if direction == "backward" {
		anchor = calendarDate(days - 1)
	}
	return url.Values{"direction": {direction}, "anchorDate": {anchor}, "timeZone": {"Asia/Shanghai"}}
}

func calendarOrderIndex(direction string, days, index int) int {
	if direction == "backward" {
		return days - 1 - index
	}
	return index
}

func calendarCursor(t *testing.T, s *Service, p Response) cursor {
	t.Helper()
	if p.NextCursor == nil {
		t.Fatal("missing continuation cursor")
	}
	c, e := s.decode(*p.NextCursor)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func assertCalendarOrder(t *testing.T, items []Item, direction string, days, rowsPerDay int) {
	t.Helper()
	if len(items) != days*rowsPerDay {
		t.Fatalf("got %d records, want %d", len(items), days*rowsPerDay)
	}
	for i, item := range items {
		day := calendarOrderIndex(direction, days, i/rowsPerDay)
		row := calendarOrderIndex(direction, rowsPerDay, i%rowsPerDay)
		if *item.StorageDate != calendarDate(day) || item.Data["id"] != fmt.Sprint(row) {
			t.Fatalf("record %d out of order: date=%s id=%v", i, *item.StorageDate, item.Data["id"])
		}
	}
}

func TestCalendarSparseFilesFillDefaultAndMaximumPage(t *testing.T) {
	for _, direction := range []string{"forward", "backward"} {
		for _, rowsPerDay := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/rows=%d", direction, rowsPerDay), func(t *testing.T) {
				s := fixture(t)
				const days = 55
				for day := 0; day < days; day++ {
					put(t, s, "calendars", calendarDate(day)+".jsonl", calendarRows(t, rowsPerDay))
				}
				q := calendarQuery(direction, days)
				want := 50
				if rowsPerDay == 2 {
					q.Set("limit", "100")
					want = 100
				}
				first := get(t, s, "calendars", q)
				if len(first.Items) != want {
					t.Fatalf("sparse first page got %d, want %d", len(first.Items), want)
				}
				c := calendarCursor(t, s, first)
				if c.FileKey != calendarDate(calendarOrderIndex(direction, days, 50)) ||
					direction == "forward" && c.AfterByte != 0 ||
					direction == "backward" && c.BeforeByte != c.SnapshotEnd {
					t.Fatalf("wrong next-file cursor: %+v", c)
				}
				// A cursor-only continuation also keeps direction/anchor and allows a new limit.
				rest := collect(t, s, "calendars", nextValues(first, 3))
				assertCalendarOrder(t, append(first.Items, rest...), direction, days, rowsPerDay)
			})
		}
	}
}

func TestCalendarCrossFileCursorAtMiddleAndFileBoundary(t *testing.T) {
	for _, direction := range []string{"forward", "backward"} {
		for _, limit := range []int{5, 6} {
			t.Run(fmt.Sprintf("%s/limit=%d", direction, limit), func(t *testing.T) {
				s := fixture(t)
				const days, rowsPerDay = 3, 3
				for day := 0; day < days; day++ {
					put(t, s, "calendars", calendarDate(day)+".jsonl", calendarRows(t, rowsPerDay))
				}
				q := calendarQuery(direction, days)
				q.Set("limit", fmt.Sprint(limit))
				first := get(t, s, "calendars", q)
				if len(first.Items) != limit {
					t.Fatalf("got %d, want %d", len(first.Items), limit)
				}
				c := calendarCursor(t, s, first)
				if c.FileKey != calendarDate(calendarOrderIndex(direction, days, limit/rowsPerDay)) {
					t.Fatalf("cursor points at wrong date: %+v", c)
				}
				if limit == 5 && (c.position() <= 0 || c.position() >= c.SnapshotEnd) {
					t.Fatal("cursor should resume inside the second file")
				}
				if limit == 6 && (direction == "forward" && c.AfterByte != 0 ||
					direction == "backward" && c.BeforeByte != c.SnapshotEnd) {
					t.Fatal("cursor should start at the third file")
				}
				next := nextValues(first, 4)
				if !reflect.DeepEqual(get(t, s, "calendars", next), get(t, s, "calendars", next)) {
					t.Fatal("unchanged files produced different results for the same cursor")
				}
				assertCalendarOrder(t, append(first.Items, collect(t, s, "calendars", next)...), direction, days, rowsPerDay)
			})
		}
	}
}

func TestCalendarDateFileBudgetAndEmptyContinuation(t *testing.T) {
	for _, direction := range []string{"forward", "backward"} {
		for _, contents := range []string{"valid", "empty", "invalid"} {
			t.Run(direction+"/"+contents, func(t *testing.T) {
				s := fixture(t)
				const days = 66
				for i := 0; i < days; i++ {
					raw := calendarRows(t, 1)
					if i < 64 {
						switch contents {
						case "empty":
							raw = nil
						case "invalid":
							raw = []byte("invalid\n")
						}
					}
					day := calendarOrderIndex(direction, days, i)
					put(t, s, "calendars", calendarDate(day)+".jsonl", raw)
				}
				q := calendarQuery(direction, days)
				q.Set("limit", "100")
				first := get(t, s, "calendars", q)
				want := 0
				if contents == "valid" {
					want = 64
				}
				if len(first.Items) != want {
					t.Fatalf("file budget returned %d records, want %d", len(first.Items), want)
				}
				c := calendarCursor(t, s, first)
				if c.FileKey != calendarDate(calendarOrderIndex(direction, days, 64)) {
					t.Fatalf("did not stop after 64 files: %+v", c)
				}
				if contents == "invalid" && warning(first, "INVALID_RECORD_SKIPPED") != 64 {
					t.Fatal("invalid rows bypassed the file budget")
				}
				last := get(t, s, "calendars", nextValues(first, 100))
				if len(last.Items) != 2 || last.NextCursor != nil {
					t.Fatalf("continuation lost data or did not terminate: %d records", len(last.Items))
				}
			})
		}
	}
}

func TestCalendarCrossFileByteAndScanBudgets(t *testing.T) {
	for _, direction := range []string{"forward", "backward"} {
		for _, budget := range []string{"response", "scan"} {
			t.Run(direction+"/"+budget, func(t *testing.T) {
				s := fixture(t)
				const days = 12
				row := calendarEvent("0")
				if budget == "response" {
					row["description"] = strings.Repeat("\x00", 8192)
				} else {
					// This field is read from disk but omitted from the small list projection.
					row["extra"] = strings.Repeat("x", 512<<10)
				}
				raw := append(marshal(t, row), '\n')
				for day := 0; day < days; day++ {
					put(t, s, "calendars", calendarDate(day)+".jsonl", raw)
				}
				q := calendarQuery(direction, days)
				q.Set("limit", "100")
				first := get(t, s, "calendars", q)
				if len(first.Items) < 2 || len(first.Items) >= days {
					t.Fatalf("cross-file %s budget not exercised: %d records", budget, len(first.Items))
				}
				if budget == "scan" && (len(first.Items) != ScanLimit/len(raw) || warning(first, "SCAN_BUDGET_REACHED") != 1) {
					t.Fatal("scan budget was not shared across date files")
				}
				c := calendarCursor(t, s, first)
				if c.FileKey != calendarDate(calendarOrderIndex(direction, days, len(first.Items))) ||
					direction == "forward" && c.AfterByte != 0 ||
					direction == "backward" && c.BeforeByte != c.SnapshotEnd {
					t.Fatal("deferred record was consumed")
				}
				assertCalendarOrder(t, append(first.Items, collect(t, s, "calendars", nextValues(first, 100))...), direction, days, 1)
			})
		}
	}
}

package assets

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/YoooClaw/cli/internal/calendar"
)

func calendarEvent(id string) map[string]any {
	return map[string]any{"id": id, "title": "Meeting", "startAt": "2026-10-22T09:00:00+08:00",
		"endAt": "2026-10-22T10:00:00+08:00", "timeZone": "Asia/Shanghai"}
}
func calendarRows(t *testing.T, n int) []byte {
	var raw []byte
	for i := 0; i < n; i++ {
		raw = append(raw, marshal(t, calendarEvent(fmt.Sprint(i)))...)
		raw = append(raw, '\n')
	}
	return raw
}

func TestCalendarForwardBackwardAndPhysicalOrder(t *testing.T) {
	s := fixture(t)
	for _, date := range []string{"2026-10-21", "2026-10-22", "2026-10-23"} {
		put(t, s, "calendars", date+".jsonl", calendarRows(t, 3))
	}
	for _, direction := range []string{"forward", "backward"} {
		q := url.Values{"limit": {"2"}, "direction": {direction}, "anchorDate": {"2026-10-22"}, "timeZone": {"Asia/Shanghai"}}
		items := collect(t, s, "calendars", q)
		if len(items) != 6 {
			t.Fatal("anchor included wrong files")
		}
		wantNext := "2026-10-23"
		wantID := "0"
		if direction == "backward" {
			wantNext = "2026-10-21"
			wantID = "2"
		}
		if *items[0].StorageDate != "2026-10-22" || *items[3].StorageDate != wantNext || items[0].Data["id"] != wantID {
			t.Fatalf("wrong direction: %+v", items)
		}
		if items[0].Data["isAllDay"] != nil || items[0].Data["reminderTimes"] != nil {
			t.Fatal("invented calendar defaults")
		}
	}
}

func TestCalendarRealStoreCrossDayEndExclusiveAndReplacement(t *testing.T) {
	s := fixture(t)
	dir := s.sources["calendars"].Dir
	store := calendar.NewStorage(dir)
	source := s.sources["calendars"]
	source.WithReadView = store.WithReadView
	s.sources["calendars"] = source
	makeBatch := func(end, collected string) *calendar.Batch {
		t.Helper()
		var payload calendar.Payload
		raw := fmt.Sprintf(`{"startDate":"2026-10-21","endDate":"2026-10-23","collectedAt":%q,"items":[
			{"id":"overnight","title":"Trip","startAt":"2026-10-21T23:00:00+08:00","endAt":%q,"timeZone":"Asia/Shanghai"},
			{"id":"another","startAt":"2026-10-21T12:00:00+08:00","endAt":"2026-10-21T13:00:00+08:00","timeZone":"Asia/Shanghai"}]}`, collected, end)
		decode(t, []byte(raw), &payload)
		b, e := calendar.Prepare(payload)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	b := makeBatch("2026-10-23T00:00:00+08:00", "2026-10-22T09:00:00+08:00")
	if _, e := store.Replace(b); e != nil {
		t.Fatal(e)
	}
	q := url.Values{"limit": {"1"}, "direction": {"forward"}, "anchorDate": {"2026-10-21"}}
	p := get(t, s, "calendars", q)
	items := collect(t, s, "calendars", q)
	if len(items) != 3 {
		t.Fatalf("cross-day expansion got %d", len(items))
	}
	if items[0].Data["id"] != items[2].Data["id"] || items[0].RecordID == items[2].RecordID {
		t.Fatal("occurrence identity incorrect")
	}
	raw, e := os.ReadFile(dir + "/2026-10-23.jsonl")
	if e != nil || len(bytes.TrimSpace(raw)) != 0 {
		t.Fatal("end not exclusive")
	}
	if _, e := store.Replace(makeBatch("2026-10-22T00:00:00+08:00", "2026-10-22T10:00:00+08:00")); e != nil {
		t.Fatal(e)
	}
	_, err := s.Read(context.Background(), "calendars", nextValues(p, 1))
	if err == nil || err.Code != "CURSOR_EXPIRED" {
		t.Fatal("replaced calendar did not expire cursor")
	}
}

func TestForwardHugeLineBoundedResume(t *testing.T) {
	s := fixture(t)
	row := calendarEvent("huge")
	row["description"] = string(bytes.Repeat([]byte{'x'}, 9<<20))
	raw := append(marshal(t, row), '\n')
	raw = append(raw, calendarRows(t, 1)...)
	put(t, s, "calendars", "2026-10-22.jsonl", raw)
	items := collect(t, s, "calendars", url.Values{"direction": {"forward"}, "anchorDate": {"2026-10-22"}, "limit": {"1"}})
	if len(items) != 2 || items[0].Status != "oversized" || items[1].Data["id"] != "0" {
		t.Fatal("forward skip lost record")
	}
}

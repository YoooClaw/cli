package calendar

import (
	"encoding/json"
	"strings"
	"testing"
)

const testCollectedAt = "2026-10-16T12:00:00+08:00"

func TestGroupByRecordTimeZoneAndExclusiveEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, zone, start, end string
		want                   []int
	}{
		{"local date instead of UTC", "Asia/Shanghai", "2026-10-16T00:30:00+08:00", "2026-10-16T01:00:00+08:00", []int{1, 0, 0}},
		{"midnight end", "Asia/Shanghai", "2026-10-16T23:00:00+08:00", "2026-10-17T00:00:00+08:00", []int{1, 0, 0}},
		{"all day", "Asia/Shanghai", "2026-10-16T00:00:00+08:00", "2026-10-18T00:00:00+08:00", []int{1, 1, 0}},
		{"clip both ends", "Asia/Shanghai", "2026-10-15T23:00:00+08:00", "2026-10-19T01:00:00+08:00", []int{1, 1, 1}},
		{"zero duration", "Asia/Shanghai", "2026-10-17T00:00:00+08:00", "2026-10-17T00:00:00+08:00", []int{0, 1, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := Record{}
			for k, v := range map[string]string{"startAt": tc.start, "endAt": tc.end, "timeZone": tc.zone} {
				item[k], _ = json.Marshal(v)
			}
			b, err := Prepare(Payload{StartDate: "2026-10-16", EndDate: "2026-10-18", CollectedAt: testCollectedAt, Items: []Record{item}})
			if err != nil {
				t.Fatal(err)
			}
			for i, f := range b.files {
				got := 0
				if len(f.data) != 0 {
					got = 1
				}
				if got != tc.want[i] {
					t.Fatalf("%s: hasRecord=%d want %d", f.date, got, tc.want[i])
				}
			}
		})
	}
}

func TestGroupUsesCalendarDaysAcrossDST(t *testing.T) {
	t.Parallel()
	item := record(t, `{"startAt":"2026-10-31T23:30:00-04:00","endAt":"2026-11-02T00:00:00-05:00","timeZone":"America/New_York"}`)
	b, err := Prepare(Payload{StartDate: "2026-10-31", EndDate: "2026-11-02", CollectedAt: testCollectedAt, Items: []Record{item}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.files[0].data) == 0 || len(b.files[1].data) == 0 || len(b.files[2].data) != 0 {
		t.Fatalf("wrong day grouping across DST: %+v", b.files)
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{}`, `null`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00"}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":null}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","items":[]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16","items":[]}`,
		`{"startDate":"../escape","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[]}`,
		`{"startDate":"2026-02-30","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[]}`,
		`{"startDate":"2026-10-17","endDate":"2026-10-16","collectedAt":"2026-10-16T12:00:00+08:00","items":[]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[null]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[{"startAt":"2026-10-16","endAt":"2026-10-17","timeZone":"Asia/Shanghai"}]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[{"startAt":"2026-10-16T10:00:00+08:00","endAt":"2026-10-16T09:00:00+08:00","timeZone":"Asia/Shanghai"}]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[{"startAt":"2026-10-16T09:00:00+08:00","endAt":"2026-10-16T10:00:00+08:00","timeZone":"+08:00"}]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[{"startAt":"2026-10-16T09:00:00+08:00","endAt":"2026-10-16T10:00:00+08:00","timeZone":"Local"}]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-17","collectedAt":"2026-10-16T12:00:00+08:00","items":[{"startAt":"2026-10-16T09:00:00Z","endAt":"2026-10-16T10:00:00Z","timeZone":"Asia/Shanghai"}]}`,
	} {
		var p Payload
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		if _, err := Prepare(p); err == nil {
			t.Errorf("accepted invalid payload: %s", body)
		}
	}
}

func TestPrepareRejectsOversizedSnapshot(t *testing.T) {
	t.Parallel()
	base := Payload{StartDate: "2026-01-01", EndDate: "2026-12-31", CollectedAt: testCollectedAt, Items: []Record{}}
	if _, err := Prepare(base); err != nil {
		t.Fatalf("366-day snapshot rejected: %v", err)
	}
	for name, p := range map[string]Payload{
		"range too long":    {StartDate: "2026-01-01", EndDate: "2027-01-02", CollectedAt: testCollectedAt, Items: []Record{}},
		"range from year 1": {StartDate: "0001-01-01", EndDate: "9999-12-31", CollectedAt: testCollectedAt, Items: []Record{}},
		"too many items":    {StartDate: "2026-01-01", EndDate: "2026-01-02", CollectedAt: testCollectedAt, Items: make([]Record, maxItems+1)},
	} {
		if _, err := Prepare(p); err == nil {
			t.Errorf("%s: accepted oversized snapshot", name)
		}
	}
}

func TestPrepareRejectsOversizedExpansion(t *testing.T) {
	t.Parallel()
	item := Record{
		"startAt":  json.RawMessage(`"2026-01-01T00:00:00+08:00"`),
		"endAt":    json.RawMessage(`"2026-12-31T00:00:00+08:00"`),
		"timeZone": json.RawMessage(`"Asia/Shanghai"`),
		"notes":    json.RawMessage(`"` + strings.Repeat("a", 64<<10) + `"`),
	}
	p := Payload{StartDate: "2026-01-01", EndDate: "2026-12-31", CollectedAt: testCollectedAt, Items: []Record{item}}
	if _, err := Prepare(p); err == nil {
		t.Fatal("cross-day expansion beyond maxPreparedBytes accepted")
	}
}

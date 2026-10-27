package healthdata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleSleep() SleepSession {
	source := "watch"
	return SleepSession{SleepSummary: SleepSummary{ID: strings.Repeat("a", 64), StartAt: "2026-09-30T23:00:00+08:00", EndAt: "2026-10-01T01:00:00+08:00", SleepMinutes: 110, StageMinutes: map[string]int{"core": 60, "deep": 0, "rem": 50, "unspecified": 0}, AwakeMinutes: 10, AwakeningCount: 1, LongestAwakeMinutes: 10, Sources: []SleepSource{{"watch", "Test Watch"}}}, Segments: []SleepSegment{
		{"core", "2026-09-30T23:00:00+08:00", "2026-10-01T00:00:00+08:00", &source},
		{"awake", "2026-10-01T00:00:00+08:00", "2026-10-01T00:10:00+08:00", &source},
		{"rem", "2026-10-01T00:10:00+08:00", "2026-10-01T01:00:00+08:00", &source},
	}}
}
func sleepBatch(t *testing.T, s SleepSession) []byte {
	t.Helper()
	raw, err := json.Marshal(DailyBatch{SchemaVersion: 3, Provider: "healthkit", TimeZone: "Asia/Shanghai", CollectedAt: "2026-10-01T12:00:00+08:00", Days: []DailySnapshot{{Date: "2026-10-01", Items: []json.RawMessage{}, SleepSessions: []SleepSession{s}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestSleepOnlyDayIsStoredCompletelyAndCorrectedByReplacement(t *testing.T) {
	dir := t.TempDir()
	store := NewStorage(dir)
	s := sampleSleep()
	b, err := Prepare(sleepBatch(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if b.ItemCount != 1 {
		t.Fatal("session not counted in receipt")
	}
	for range 2 {
		if err = store.Append(b); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-10-01.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	day, _, err := ReadSnapshot(raw, "2026-10-01")
	if err != nil || len(day.SleepSessions) != 1 || len(day.SleepSessions[0].Segments) != 3 {
		t.Fatal("session not preserved", err)
	}
	if day.SleepSessions[0].StartAt != s.StartAt || day.SleepSessions[0].SleepMinutes != 110 {
		t.Fatal("overnight clipped")
	}
}
func TestSleepRejectsInvalidSummariesAndTimelines(t *testing.T) {
	cases := map[string]func(*SleepSession){
		"wrong sleep total": func(s *SleepSession) { s.SleepMinutes++ },
		"wrong stage":       func(s *SleepSession) { s.StageMinutes["rem"]++ },
		"missing stage":     func(s *SleepSession) { delete(s.StageMinutes, "deep"); s.StageMinutes["bad"] = 0 },
		"wrong awake":       func(s *SleepSession) { s.AwakeMinutes++ },
		"wrong count":       func(s *SleepSession) { s.AwakeningCount++ },
		"wrong longest":     func(s *SleepSession) { s.LongestAwakeMinutes++ },
		"wrong gap":         func(s *SleepSession) { s.UnknownMinutes++ },
		"bad id":            func(s *SleepSession) { s.ID = "../escape" },
		"missing source":    func(s *SleepSession) { s.Sources = nil },
		"duplicate source":  func(s *SleepSession) { s.Sources = append(s.Sources, s.Sources[0]) },
		"invalid stage":     func(s *SleepSession) { s.Segments[1].Stage = "inBed" },
		"gap omitted":       func(s *SleepSession) { s.Segments = s.Segments[:2] },
		"overlap":           func(s *SleepSession) { s.Segments[1].StartAt = "2026-09-30T23:59:00+08:00" },
		"out of order":      func(s *SleepSession) { s.Segments[0], s.Segments[1] = s.Segments[1], s.Segments[0] },
		"wrong day":         func(s *SleepSession) { s.EndAt = "2026-10-02T01:00:00+08:00" },
		"source missing":    func(s *SleepSession) { s.Segments[1].Source = nil },
		"huge timeline":     func(s *SleepSession) { s.Segments = make([]SleepSegment, MaxSleepSegments+1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := sampleSleep()
			change(&s)
			if _, err := Prepare(sleepBatch(t, s)); err == nil {
				t.Fatal("accepted invalid sleep")
			}
		})
	}
}
func TestUnknownGapIsSeparateFromAwakeAndStageTotals(t *testing.T) {
	s := sampleSleep()
	s.Segments[1].Stage = "unknown"
	s.Segments[1].Source = nil
	s.AwakeMinutes = 0
	s.AwakeningCount = 0
	s.LongestAwakeMinutes = 0
	s.UnknownMinutes = 10
	if _, err := Prepare(sleepBatch(t, s)); err != nil {
		t.Fatal(err)
	}
}
func TestMillisecondPrecisionRoundsEndpointsAndKeepsZeroAwakening(t *testing.T) {
	s := sampleSleep()
	s.StartAt = "2026-10-01T00:00:01.001+08:00"
	s.EndAt = "2026-10-01T00:01:31.001+08:00"
	s.Segments[0].StartAt = s.StartAt
	s.Segments[0].EndAt = "2026-10-01T00:00:31.001+08:00"
	s.Segments[1].StartAt = s.Segments[0].EndAt
	s.Segments[1].EndAt = "2026-10-01T00:01:01.001+08:00"
	s.Segments[2].StartAt = s.Segments[1].EndAt
	s.Segments[2].EndAt = s.EndAt
	s.SleepMinutes = 2
	s.StageMinutes["core"] = 1
	s.StageMinutes["rem"] = 1
	s.AwakeMinutes = 0
	s.LongestAwakeMinutes = 0
	if _, err := Prepare(sleepBatch(t, s)); err != nil {
		t.Fatal(err)
	}
}

func TestV3DayBoundsAndRequiredArrays(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"missing sessions":  func(day map[string]any) { delete(day, "sleepSessions") },
		"null sessions":     func(day map[string]any) { day["sleepSessions"] = nil },
		"missing items":     func(day map[string]any) { delete(day, "items") },
		"too many sessions": func(day map[string]any) { day["sleepSessions"] = make([]SleepSession, MaxSleepSessions+1) },
		"too many metrics":  func(day map[string]any) { day["items"] = make([]map[string]any, 257) },
		"day bytes": func(day map[string]any) {
			day["items"] = []map[string]any{{"metric": "steps", "name": strings.Repeat("x", MaxDayBytes), "value": 1, "unit": "steps"}}
		},
		"old sleep metric": func(day map[string]any) {
			day["items"] = []map[string]any{{"metric": "sleep_minutes", "name": "Sleep", "value": 1, "unit": "min"}}
		},
		"extra field": func(day map[string]any) { day["unexpected"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(sleepBatch(t, sampleSleep()), &body); err != nil {
				t.Fatal(err)
			}
			change(body["days"].([]any)[0].(map[string]any))
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Prepare(raw); err == nil {
				t.Fatal("accepted invalid v3 day")
			}
		})
	}
}

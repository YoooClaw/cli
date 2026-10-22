package healthdata

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxSleepSegments = 4096
const MaxSleepSessions = 32
const MaxDayBytes = 4 << 20

var SleepIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type SleepSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type SleepSegment struct {
	Stage   string  `json:"stage"`
	StartAt string  `json:"startAt"`
	EndAt   string  `json:"endAt"`
	Source  *string `json:"source,omitempty"`
}
type SleepSummary struct {
	ID                  string         `json:"id"`
	StartAt             string         `json:"startAt"`
	EndAt               string         `json:"endAt"`
	SleepMinutes        int            `json:"sleepMinutes"`
	StageMinutes        map[string]int `json:"stageMinutes"`
	AwakeMinutes        int            `json:"awakeMinutes"`
	AwakeningCount      int            `json:"awakeningCount"`
	LongestAwakeMinutes int            `json:"longestAwakeMinutes"`
	UnknownMinutes      int            `json:"unknownMinutes"`
	IsPartial           bool           `json:"isPartial"`
	Sources             []SleepSource  `json:"sources"`
}
type SleepSession struct {
	SleepSummary
	Segments []SleepSegment `json:"segments"`
}
type SleepListSummary struct {
	SleepSummary
	SegmentCount int `json:"segmentCount"`
}

func (s SleepSession) Summary() SleepListSummary {
	return SleepListSummary{s.SleepSummary, len(s.Segments)}
}
func asleep(stage string) bool {
	return stage == "core" || stage == "deep" || stage == "rem" || stage == "unspecified"
}
func minuteDuration(a, b time.Time) int {
	return int(b.Round(time.Minute).Sub(a.Round(time.Minute)) / time.Minute)
}

func validateSleep(s SleepSession, date string, zone *time.Location, collected time.Time) error {
	bad := func() error { return fmt.Errorf("invalid sleep session %q", s.ID) }
	if !SleepIDPattern.MatchString(s.ID) || len(s.Segments) < 1 || len(s.Segments) > MaxSleepSegments || len(s.Sources) < 1 || len(s.Sources) > 32 {
		return bad()
	}
	start, e1 := time.Parse(time.RFC3339Nano, s.StartAt)
	end, e2 := time.Parse(time.RFC3339Nano, s.EndAt)
	if e1 != nil || e2 != nil || !start.Before(end) || end.After(collected) || end.In(zone).Format(time.DateOnly) != date {
		return bad()
	}
	if !asleep(s.Segments[0].Stage) || !asleep(s.Segments[len(s.Segments)-1].Stage) {
		return bad()
	}
	sources := map[string]bool{}
	for _, src := range s.Sources {
		if strings.TrimSpace(src.ID) == "" || len(src.ID) > 512 || strings.TrimSpace(src.Name) == "" || len(src.Name) > 512 || sources[src.ID] {
			return bad()
		}
		sources[src.ID] = true
	}
	durations := map[string]int{"core": 0, "deep": 0, "rem": 0, "unspecified": 0}
	position := start
	var lastSleepEnd, awakeStart time.Time
	awake, unknown, count, longest := 0, 0, 0, 0
	for _, seg := range s.Segments {
		a, ea := time.Parse(time.RFC3339Nano, seg.StartAt)
		b, eb := time.Parse(time.RFC3339Nano, seg.EndAt)
		if ea != nil || eb != nil || !a.Equal(position) || !a.Before(b) || b.After(end) {
			return bad()
		}
		if seg.Stage == "unknown" {
			if seg.Source != nil {
				return bad()
			}
		} else if seg.Source == nil || !sources[*seg.Source] {
			return bad()
		}
		n := minuteDuration(a, b)
		switch {
		case asleep(seg.Stage):
			if !lastSleepEnd.IsZero() && a.Sub(lastSleepEnd) > time.Hour {
				return bad()
			}
			lastSleepEnd = b
			durations[seg.Stage] += n
		case seg.Stage == "awake":
			awake += n
			if awakeStart.IsZero() {
				awakeStart = a
				count++
			}
		case seg.Stage == "unknown":
			unknown += n
		default:
			return bad()
		}
		if seg.Stage != "awake" && !awakeStart.IsZero() {
			longest = max(longest, minuteDuration(awakeStart, a))
			awakeStart = time.Time{}
		}
		position = b
	}
	total := 0
	for stage, n := range durations {
		if value, exists := s.StageMinutes[stage]; !exists || value != n {
			return bad()
		}
		total += n
	}
	if !position.Equal(end) || len(s.StageMinutes) != 4 || s.SleepMinutes != total || s.AwakeMinutes != awake || s.UnknownMinutes != unknown || s.AwakeningCount != count || s.LongestAwakeMinutes != longest {
		return bad()
	}
	return nil
}

// ReadSnapshot validates the stored v3 JSONL and exposes complete sessions to bounded readers.
func ReadSnapshot(raw []byte, date string) (DailySnapshot, dailyHeader, error) {
	var header dailyHeader
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || json.Unmarshal([]byte(lines[0]), &header) != nil || header.SchemaVersion != 3 || header.Date != date {
		return DailySnapshot{}, header, fmt.Errorf("unsupported or invalid health snapshot")
	}
	day := DailySnapshot{Date: date, Items: []json.RawMessage{}, SleepSessions: []SleepSession{}}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var tag struct {
			Type    string          `json:"type"`
			Session json.RawMessage `json:"session"`
		}
		if json.Unmarshal([]byte(line), &tag) != nil {
			return day, header, fmt.Errorf("invalid snapshot line")
		}
		if tag.Type == "sleepSession" {
			var session SleepSession
			if err := json.Unmarshal(tag.Session, &session); err != nil {
				return day, header, err
			}
			day.SleepSessions = append(day.SleepSessions, session)
		} else if tag.Type != "" {
			return day, header, fmt.Errorf("unknown snapshot record")
		} else {
			day.Items = append(day.Items, json.RawMessage(line))
		}
	}
	batch := DailyBatch{SchemaVersion: 3, Provider: header.Provider, TimeZone: header.TimeZone, CollectedAt: header.UpdatedAt, Days: []DailySnapshot{day}}
	payload, err := json.Marshal(batch)
	if err != nil {
		return day, header, err
	}
	_, _, _, err = prepareDaily(payload)
	return day, header, err
}

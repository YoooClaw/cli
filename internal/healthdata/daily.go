package healthdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DailyBatch is a complete readable snapshot for each supplied local calendar day.
// Provider and collection time are stored once per file, never repeated on each metric.
type DailyBatch struct {
	SchemaVersion int             `json:"schemaVersion"`
	Provider      string          `json:"provider"`
	CollectedAt   string          `json:"collectedAt"`
	TimeZone      string          `json:"timeZone"`
	Days          []DailySnapshot `json:"days"`
}
type DailySnapshot struct {
	Date          string            `json:"date"`
	Items         []json.RawMessage `json:"items"`
	SleepSessions []SleepSession    `json:"sleepSessions"`
}
type dailyHeader struct {
	SchemaVersion int    `json:"schemaVersion"`
	Date          string `json:"date"`
	TimeZone      string `json:"timeZone"`
	UpdatedAt     string `json:"updatedAt"`
	Provider      string `json:"provider"`
}

var metricPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

func prepareDaily(raw json.RawMessage) (*DailyBatch, time.Time, int, error) {
	var b DailyBatch
	if !json.Valid(raw) {
		return nil, time.Time{}, 0, fmt.Errorf("body must be one JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("invalid daily snapshot: %w", err)
	}
	if b.SchemaVersion != 3 || strings.TrimSpace(b.Provider) == "" {
		return nil, time.Time{}, 0, fmt.Errorf("schemaVersion=3 and provider are required")
	}
	collected, err := time.Parse(time.RFC3339Nano, b.CollectedAt)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("collectedAt must include an RFC3339 timezone offset")
	}
	if b.TimeZone == "" {
		return nil, time.Time{}, 0, fmt.Errorf("timeZone is required")
	}
	zone, zoneErr := time.LoadLocation(b.TimeZone)
	if zoneErr != nil {
		return nil, time.Time{}, 0, fmt.Errorf("timeZone must be an IANA timezone")
	}
	if len(b.Days) == 0 || len(b.Days) > 366 {
		return nil, time.Time{}, 0, fmt.Errorf("days must contain 1..366 daily snapshots")
	}
	dates := map[string]bool{}
	count := 0
	for n, d := range b.Days {
		date, err := time.Parse("2006-01-02", d.Date)
		if err != nil || date.Format("2006-01-02") != d.Date || dates[d.Date] {
			return nil, time.Time{}, 0, fmt.Errorf("days[%d] has an invalid or repeated date", n)
		}
		dates[d.Date] = true
		if d.Items == nil || d.SleepSessions == nil || len(d.Items) > 256 || len(d.SleepSessions) > MaxSleepSessions {
			return nil, time.Time{}, 0, fmt.Errorf("days[%d].items/sleepSessions arrays are required and must respect their limits", n)
		}
		if len(d.Items) == 0 && len(d.SleepSessions) == 0 {
			return nil, time.Time{}, 0, fmt.Errorf("empty snapshots are not deletions")
		}
		encoded, _ := json.Marshal(d)
		if len(encoded) > MaxDayBytes-4096 {
			return nil, time.Time{}, 0, fmt.Errorf("health day exceeds 4 MiB")
		}
		seenSessions := map[string]bool{}
		var previousEnd time.Time
		for _, session := range d.SleepSessions {
			if seenSessions[session.ID] {
				return nil, time.Time{}, 0, fmt.Errorf("duplicate sleep session")
			}
			seenSessions[session.ID] = true
			if err := validateSleep(session, d.Date, zone, collected); err != nil {
				return nil, time.Time{}, 0, err
			}
			start, _ := time.Parse(time.RFC3339Nano, session.StartAt)
			if !previousEnd.IsZero() && !start.After(previousEnd) {
				return nil, time.Time{}, 0, fmt.Errorf("sleep sessions must be ordered and disjoint")
			}
			previousEnd, _ = time.Parse(time.RFC3339Nano, session.EndAt)
			count++
		}
		metrics := map[string]bool{}
		for j, item := range d.Items {
			var m struct {
				Metric string       `json:"metric"`
				Name   string       `json:"name"`
				Value  *json.Number `json:"value"`
				Unit   string       `json:"unit"`
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(item, &keys); err != nil || len(keys) != 4 {
				return nil, time.Time{}, 0, fmt.Errorf("days[%d].items[%d] must contain only metric/name/value/unit", n, j)
			}
			if err := json.Unmarshal(item, &m); err != nil || !metricPattern.MatchString(m.Metric) || metrics[m.Metric] || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Unit) == "" || m.Value == nil || len(keys["value"]) == 0 || bytes.TrimSpace(keys["value"])[0] == '"' {
				return nil, time.Time{}, 0, fmt.Errorf("days[%d].items[%d] has invalid or duplicate metric fields", n, j)
			}
			// Numbers remain RawMessage on disk. Float64 is used only to reject non-finite/out-of-range input.
			if _, err := m.Value.Float64(); err != nil {
				return nil, time.Time{}, 0, fmt.Errorf("days[%d].items[%d].value must be a finite number", n, j)
			}
			if strings.HasPrefix(m.Metric, "sleep_") {
				return nil, time.Time{}, 0, fmt.Errorf("sleep data belongs in sleepSessions")
			}
			metrics[m.Metric] = true
			count++
		}
	}
	return &b, collected, count, nil
}

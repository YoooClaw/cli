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
	Date  string            `json:"date"`
	Items []json.RawMessage `json:"items"`
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
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("invalid daily snapshot: %w", err)
	}
	if b.SchemaVersion != 2 || strings.TrimSpace(b.Provider) == "" {
		return nil, time.Time{}, 0, fmt.Errorf("schemaVersion=2 and provider are required")
	}
	collected, err := time.Parse(time.RFC3339Nano, b.CollectedAt)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("collectedAt must include an RFC3339 timezone offset")
	}
	if b.TimeZone == "" {
		return nil, time.Time{}, 0, fmt.Errorf("timeZone is required")
	}
	if _, err := time.LoadLocation(b.TimeZone); err != nil {
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
		if len(d.Items) == 0 || len(d.Items) > 256 {
			return nil, time.Time{}, 0, fmt.Errorf("days[%d].items must contain 1..256 metrics; an empty read is not a deletion", n)
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
			metrics[m.Metric] = true
			count++
		}
	}
	return &b, collected, count, nil
}

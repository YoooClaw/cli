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
	Medications   []Medication      `json:"medications,omitempty"`
	Workouts      []Workout         `json:"workouts,omitempty"`
	IntakeRecords []IntakeRecord    `json:"intakeRecords,omitempty"`
	Moods         []Mood            `json:"moods,omitempty"`
}
type dailyHeader struct {
	SchemaVersion int    `json:"schemaVersion"`
	Date          string `json:"date"`
	TimeZone      string `json:"timeZone"`
	UpdatedAt     string `json:"updatedAt"`
	Provider      string `json:"provider"`
}

// rawDailyEnvelope keeps each day undecoded so one malformed day cannot fail
// validation of the other days in the same request.
type rawDailyEnvelope struct {
	SchemaVersion int               `json:"schemaVersion"`
	Provider      string            `json:"provider"`
	CollectedAt   string            `json:"collectedAt"`
	TimeZone      string            `json:"timeZone"`
	Days          []json.RawMessage `json:"days"`
}

// rawDailySnapshot keeps each category undecoded so a bad record can be
// dropped without losing the rest of the day.
type rawDailySnapshot struct {
	Date          string          `json:"date"`
	Items         json.RawMessage `json:"items"`
	SleepSessions json.RawMessage `json:"sleepSessions"`
	Medications   json.RawMessage `json:"medications"`
	Workouts      json.RawMessage `json:"workouts"`
	IntakeRecords json.RawMessage `json:"intakeRecords"`
	Moods         json.RawMessage `json:"moods"`
}

var metricPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

// maxDayWarnings bounds the per-day receipt and log line. Dropped counts stay exact.
const maxDayWarnings = 20

// prepareDaily validates the request envelope as a whole but each day on its
// own. A day that fails validation is still returned in the batch, with its
// outcome at the same index, so callers can report a per-day receipt.
func prepareDaily(raw json.RawMessage) (*DailyBatch, time.Time, []dayInfo, int, error) {
	if !json.Valid(raw) {
		return nil, time.Time{}, nil, 0, fmt.Errorf("body must be one JSON value")
	}
	var envelope rawDailyEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, time.Time{}, nil, 0, fmt.Errorf("invalid daily snapshot: %w", err)
	}
	provider := normalizeProvider(envelope.Provider)
	if envelope.SchemaVersion != 3 || provider == "" {
		return nil, time.Time{}, nil, 0, fmt.Errorf("schemaVersion=3 and provider are required")
	}
	collected, err := time.Parse(time.RFC3339Nano, envelope.CollectedAt)
	if err != nil {
		return nil, time.Time{}, nil, 0, fmt.Errorf("collectedAt must include an RFC3339 timezone offset")
	}
	if envelope.TimeZone == "" {
		return nil, time.Time{}, nil, 0, fmt.Errorf("timeZone is required")
	}
	zone, zoneErr := time.LoadLocation(envelope.TimeZone)
	if zoneErr != nil {
		return nil, time.Time{}, nil, 0, fmt.Errorf("timeZone must be an IANA timezone")
	}
	if len(envelope.Days) == 0 || len(envelope.Days) > 366 {
		return nil, time.Time{}, nil, 0, fmt.Errorf("days must contain 1..366 daily snapshots")
	}
	b := &DailyBatch{
		SchemaVersion: envelope.SchemaVersion,
		Provider:      provider,
		CollectedAt:   envelope.CollectedAt,
		TimeZone:      envelope.TimeZone,
		Days:          make([]DailySnapshot, 0, len(envelope.Days)),
	}
	infos := make([]dayInfo, 0, len(envelope.Days))
	dates := map[string]bool{}
	count := 0
	for n, rawDay := range envelope.Days {
		var rd rawDailySnapshot
		dayDecoder := json.NewDecoder(bytes.NewReader(rawDay))
		dayDecoder.DisallowUnknownFields()
		if decodeErr := dayDecoder.Decode(&rd); decodeErr != nil {
			b.Days = append(b.Days, DailySnapshot{Date: rawDayDate(rawDay)})
			infos = append(infos, dayInfo{err: fmt.Errorf("days[%d]: invalid daily snapshot: %w", n, decodeErr)})
			continue
		}
		d, info := prepareDay(rd, n, dates, zone, collected)
		b.Days = append(b.Days, d)
		infos = append(infos, info)
		count += info.count
	}
	return b, collected, infos, count, nil
}

// rawDayDate recovers the client-supplied date for a day whose full decode
// failed, so the receipt can still point at the offending day.
func rawDayDate(raw json.RawMessage) string {
	var meta struct {
		Date string `json:"date"`
	}
	_ = json.Unmarshal(raw, &meta)
	return meta.Date
}

func isAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// decodeRequiredCategory rejects a malformed mandatory category at day level.
func decodeRequiredCategory(raw json.RawMessage, section string, n int) ([]json.RawMessage, error) {
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("days[%d].%s must be an array", n, section)
	}
	return records, nil
}

// decodeCategory reports category-level structural problems separately from
// record-level ones; the category is dropped, not the day.
func decodeCategory(raw json.RawMessage, section string, n int, info *dayInfo) ([]json.RawMessage, bool) {
	if isAbsent(raw) {
		return nil, true
	}
	var records []json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		info.warnings = append(info.warnings, fmt.Sprintf("days[%d].%s: category dropped: %v", n, section, err))
		return nil, false
	}
	return records, true
}

func prepareDay(rd rawDailySnapshot, n int, dates map[string]bool, zone *time.Location, collected time.Time) (DailySnapshot, dayInfo) {
	info := dayInfo{}
	d := DailySnapshot{Date: rd.Date, Items: []json.RawMessage{}, SleepSessions: []SleepSession{}}
	date, err := time.Parse("2006-01-02", rd.Date)
	if err != nil || date.Format("2006-01-02") != rd.Date || dates[rd.Date] {
		info.err = fmt.Errorf("days[%d] has an invalid or repeated date", n)
		return d, info
	}
	// A missing mandatory category would erase that category from the replaced
	// day file, so it stays a day-level error rather than a silent drop.
	if isAbsent(rd.Items) || isAbsent(rd.SleepSessions) {
		info.err = fmt.Errorf("days[%d].items/sleepSessions arrays are required", n)
		return d, info
	}
	// Same for a malformed mandatory category: dropping it would delete a whole
	// previously stored category, which a garbage shape cannot be assumed to mean.
	items, itemErr := decodeRequiredCategory(rd.Items, "items", n)
	sessions, sleepErr := decodeRequiredCategory(rd.SleepSessions, "sleepSessions", n)
	if itemErr != nil {
		info.err = itemErr
		return d, info
	}
	if sleepErr != nil {
		info.err = sleepErr
		return d, info
	}
	d.Items = filterItems(items, n, &info)
	d.SleepSessions = filterSleep(sessions, n, d.Date, zone, collected, &info)
	d.Medications = filterDetailCategory(rd.Medications, "medications", n, &info, func(records []json.RawMessage) ([]Medication, int, []string) {
		return filterMedications(records, n, d.Date, zone, collected)
	})
	d.Workouts = filterDetailCategory(rd.Workouts, "workouts", n, &info, func(records []json.RawMessage) ([]Workout, int, []string) {
		return filterWorkouts(records, n, d.Date, zone, collected)
	})
	d.IntakeRecords = filterDetailCategory(rd.IntakeRecords, "intakeRecords", n, &info, func(records []json.RawMessage) ([]IntakeRecord, int, []string) {
		return filterIntake(records, n, d.Date, zone, collected)
	})
	d.Moods = filterDetailCategory(rd.Moods, "moods", n, &info, func(records []json.RawMessage) ([]Mood, int, []string) {
		return filterMoods(records, n, d.Date, zone, collected)
	})
	if len(d.Items) == 0 && len(d.SleepSessions) == 0 && len(d.Medications) == 0 && len(d.Workouts) == 0 && len(d.IntakeRecords) == 0 && len(d.Moods) == 0 {
		if info.dropped > 0 || len(info.warnings) > 0 {
			// Name the first offending record so the day-level receipt alone
			// explains why nothing survived.
			detail := ""
			if len(info.warnings) > 0 {
				detail = ": " + info.warnings[0]
			}
			info.err = fmt.Errorf("days[%d]: no valid records remain after dropping invalid records%s", n, detail)
		} else {
			info.err = fmt.Errorf("days[%d]: empty snapshots are not deletions", n)
		}
		info.warnings = capWarnings(info.warnings)
		return d, info
	}
	encoded, _ := json.Marshal(d)
	if len(encoded) > MaxDayBytes-4096 {
		info.err = fmt.Errorf("days[%d]: health day exceeds 4 MiB", n)
		info.warnings = capWarnings(info.warnings)
		return d, info
	}
	// Only a day that is actually written claims its date, so an earlier
	// rejected duplicate cannot block a later valid one.
	dates[d.Date] = true
	info.count = len(d.Items) + len(d.SleepSessions) + len(d.Medications) + len(d.Workouts) + len(d.IntakeRecords) + len(d.Moods)
	info.warnings = capWarnings(info.warnings)
	return d, info
}

// filterDetailCategory applies a detail filter to a raw category and merges its
// warnings and dropped-record count into the day.
func filterDetailCategory[T any](raw json.RawMessage, section string, n int, info *dayInfo, filter func([]json.RawMessage) ([]T, int, []string)) []T {
	records, ok := decodeCategory(raw, section, n, info)
	if !ok {
		return nil
	}
	kept, dropped, warnings := filter(records)
	info.dropped += dropped
	info.warnings = append(info.warnings, warnings...)
	return kept
}

func filterItems(records []json.RawMessage, n int, info *dayInfo) []json.RawMessage {
	kept := make([]json.RawMessage, 0, len(records))
	metrics := map[string]bool{}
	for j, item := range records {
		if len(kept) >= 256 {
			excess := len(records) - j
			info.dropped += excess
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].items: %d records exceed limit 256", n, excess))
			break
		}
		if err := validateItem(item); err != nil {
			info.dropped++
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].items[%d]: %v", n, j, err))
			continue
		}
		var m struct {
			Metric string `json:"metric"`
		}
		_ = json.Unmarshal(item, &m)
		if metrics[m.Metric] {
			info.dropped++
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].items[%d]: duplicate metric", n, j))
			continue
		}
		metrics[m.Metric] = true
		kept = append(kept, item)
	}
	return kept
}

func validateItem(item json.RawMessage) error {
	var m struct {
		Metric string       `json:"metric"`
		Name   string       `json:"name"`
		Value  *json.Number `json:"value"`
		Unit   string       `json:"unit"`
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(item, &keys); err != nil || len(keys) != 4 {
		return fmt.Errorf("must contain only metric/name/value/unit")
	}
	if err := json.Unmarshal(item, &m); err != nil || !metricPattern.MatchString(m.Metric) || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Unit) == "" || m.Value == nil || len(keys["value"]) == 0 || bytes.TrimSpace(keys["value"])[0] == '"' {
		return fmt.Errorf("has invalid metric fields")
	}
	// Numbers remain RawMessage on disk. Float64 is used only to reject non-finite/out-of-range input.
	if _, err := m.Value.Float64(); err != nil {
		return fmt.Errorf("value must be a finite number")
	}
	if strings.HasPrefix(m.Metric, "sleep_") {
		return fmt.Errorf("sleep data belongs in sleepSessions")
	}
	return nil
}

func filterSleep(records []json.RawMessage, n int, date string, zone *time.Location, collected time.Time, info *dayInfo) []SleepSession {
	kept := make([]SleepSession, 0, len(records))
	seen := map[string]bool{}
	for i, record := range records {
		if len(kept) >= MaxSleepSessions {
			excess := len(records) - i
			info.dropped += excess
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].sleepSessions: %d sessions exceed limit %d", n, excess, MaxSleepSessions))
			break
		}
		var s SleepSession
		if err := decodeRecord(record, &s); err != nil {
			info.dropped++
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].sleepSessions[%d]: invalid sleep session: %v", n, i, err))
			continue
		}
		if seen[s.ID] || validateSleep(s, date, zone, collected) != nil {
			info.dropped++
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].sleepSessions[%d]: invalid sleep session", n, i))
			continue
		}
		seen[s.ID] = true
		kept = append(kept, s)
	}
	// Ordering is a property of the surviving sessions, so a bad neighbour
	// cannot invalidate an otherwise well-formed timeline.
	ordered := make([]SleepSession, 0, len(kept))
	var previousEnd time.Time
	for i, s := range kept {
		start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
		if !previousEnd.IsZero() && !start.After(previousEnd) {
			info.dropped++
			info.warnings = append(info.warnings, fmt.Sprintf("days[%d].sleepSessions[%d]: must be ordered and disjoint", n, i))
			continue
		}
		ordered = append(ordered, s)
		previousEnd, _ = time.Parse(time.RFC3339Nano, s.EndAt)
	}
	return ordered
}

func capWarnings(warnings []string) []string {
	if len(warnings) <= maxDayWarnings {
		return warnings
	}
	capped := append([]string(nil), warnings[:maxDayWarnings]...)
	return append(capped, fmt.Sprintf("(+%d more)", len(warnings)-maxDayWarnings))
}

// Providers are an open namespace; normalization does not authenticate a source.
func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func isAppleProvider(provider string) bool { return normalizeProvider(provider) == "healthkit" }

package healthdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Optional v3 details preserve the existing envelope and four-field daily metrics.
type Medication struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Nickname    *string          `json:"nickname,omitempty"`
	Form        string           `json:"form"`
	IsArchived  bool             `json:"isArchived"`
	HasSchedule bool             `json:"hasSchedule"`
	DoseEvents  []MedicationDose `json:"doseEvents"`
}
type MedicationDose struct {
	ID                string   `json:"id"`
	StartAt           string   `json:"startAt"`
	EndAt             string   `json:"endAt"`
	Status            string   `json:"status"`
	ScheduleType      string   `json:"scheduleType"`
	ScheduledAt       *string  `json:"scheduledAt,omitempty"`
	ScheduledQuantity *float64 `json:"scheduledQuantity,omitempty"`
	Quantity          *float64 `json:"quantity,omitempty"`
	Unit              string   `json:"unit"`
}
type DetailMetric struct {
	Metric string  `json:"metric"`
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
}
type Workout struct {
	ID              string         `json:"id"`
	Activity        string         `json:"activity"`
	Name            string         `json:"name"`
	StartAt         string         `json:"startAt"`
	EndAt           string         `json:"endAt"`
	DurationMinutes float64        `json:"durationMinutes"`
	Items           []DetailMetric `json:"items"`
}
type IntakeRecord struct {
	ID      string  `json:"id"`
	StartAt string  `json:"startAt"`
	EndAt   string  `json:"endAt"`
	Metric  string  `json:"metric"`
	Name    string  `json:"name"`
	Value   float64 `json:"value"`
	Unit    string  `json:"unit"`
}
type Mood struct {
	ID           string   `json:"id"`
	StartAt      string   `json:"startAt"`
	EndAt        string   `json:"endAt"`
	Kind         string   `json:"kind"`
	Valence      float64  `json:"valence"`
	Labels       []string `json:"labels"`
	Associations []string `json:"associations"`
}

const (
	maxMedicationsPerDay = 128
	maxWorkoutsPerDay    = 128
	maxIntakePerDay      = 4096
	maxMoodsPerDay       = 1024
	maxDosesPerDay       = 4096
	maxWorkoutMetrics    = 32
	maxMoodLabels        = 64
	maxMoodAssociations  = 32
)

func detailText(s string, maximum int) bool { return strings.TrimSpace(s) != "" && len(s) <= maximum }
func finiteNonnegative(n float64) bool      { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 }
func optionalQuantity(n *float64) bool      { return n == nil || finiteNonnegative(*n) }

// decodeRecord keeps each record's structural errors local to that record.
func decodeRecord(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func validateInterval(id, start, end, date string, zone *time.Location, collected time.Time) error {
	a, ea := time.Parse(time.RFC3339Nano, start)
	b, eb := time.Parse(time.RFC3339Nano, end)
	if !detailText(id, 256) || ea != nil || eb != nil || b.Before(a) || a.After(collected) || a.In(zone).Format("2006-01-02") != date {
		return fmt.Errorf("invalid health detail identity, date or interval")
	}
	return nil
}

func validDoseStatus(status string) bool {
	switch status {
	case "not_interacted", "notification_not_sent", "snoozed", "taken", "skipped", "not_logged":
		return true
	}
	return strings.HasPrefix(status, "unknown_")
}

func validDose(e MedicationDose, date string, zone *time.Location, collected time.Time) error {
	if err := validateInterval(e.ID, e.StartAt, e.EndAt, date, zone, collected); err != nil {
		return err
	}
	if !validDoseStatus(e.Status) || !detailText(e.Unit, 128) || !optionalQuantity(e.Quantity) || !optionalQuantity(e.ScheduledQuantity) {
		return fmt.Errorf("invalid medication dose")
	}
	switch e.ScheduleType {
	case "scheduled":
		if e.ScheduledAt == nil || e.ScheduledQuantity == nil {
			return fmt.Errorf("scheduled dose requires schedule information")
		}
		if _, err := time.Parse(time.RFC3339Nano, *e.ScheduledAt); err != nil {
			return fmt.Errorf("invalid scheduledAt")
		}
	case "as_needed":
		if e.ScheduledAt != nil || e.ScheduledQuantity != nil {
			return fmt.Errorf("invalid medication schedule type")
		}
	default:
		return fmt.Errorf("invalid medication schedule type")
	}
	return nil
}

// filterMedications drops only invalid medications and dose events so one bad
// record cannot take down the rest of the day.
func filterMedications(records []json.RawMessage, n int, date string, zone *time.Location, collected time.Time) ([]Medication, int, []string) {
	kept := make([]Medication, 0, len(records))
	seen := map[string]bool{}
	seenDoses := map[string]bool{}
	dropped := 0
	warnings := []string{}
	totalDoses := 0
	for i, record := range records {
		if len(kept) >= maxMedicationsPerDay {
			excess := len(records) - i
			dropped += excess
			warnings = append(warnings, fmt.Sprintf("days[%d].medications: %d records exceed limit %d", n, excess, maxMedicationsPerDay))
			break
		}
		var m Medication
		if err := decodeRecord(record, &m); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].medications[%d]: %v", n, i, err))
			continue
		}
		if !detailText(m.ID, 256) || seen["medication:"+m.ID] || !detailText(m.Name, 2048) || !detailText(m.Form, 128) || (m.Nickname != nil && len(*m.Nickname) > 2048) || m.DoseEvents == nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].medications[%d]: invalid medication", n, i))
			continue
		}
		doses := make([]MedicationDose, 0, len(m.DoseEvents))
		for j, e := range m.DoseEvents {
			if totalDoses+len(doses) >= maxDosesPerDay {
				dropped++
				warnings = append(warnings, fmt.Sprintf("days[%d].medications[%d].doseEvents[%d]: dose limit %d reached", n, i, j, maxDosesPerDay))
				continue
			}
			if seenDoses["dose:"+e.ID] || validDose(e, date, zone, collected) != nil {
				dropped++
				warnings = append(warnings, fmt.Sprintf("days[%d].medications[%d].doseEvents[%d]: invalid medication dose", n, i, j))
				continue
			}
			seenDoses["dose:"+e.ID] = true
			doses = append(doses, e)
		}
		totalDoses += len(doses)
		// Only today's list may include medications without an actual historical dose event.
		if len(doses) == 0 && date != collected.In(zone).Format("2006-01-02") {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].medications[%d]: historical medication requires a dose event", n, i))
			continue
		}
		seen["medication:"+m.ID] = true
		m.DoseEvents = doses
		kept = append(kept, m)
	}
	return kept, dropped, warnings
}

// filterWorkouts removes invalid workouts and, inside a workout, invalid or
// duplicate metrics.
func filterWorkouts(records []json.RawMessage, n int, date string, zone *time.Location, collected time.Time) ([]Workout, int, []string) {
	kept := make([]Workout, 0, len(records))
	seen := map[string]bool{}
	dropped := 0
	warnings := []string{}
	for i, record := range records {
		if len(kept) >= maxWorkoutsPerDay {
			excess := len(records) - i
			dropped += excess
			warnings = append(warnings, fmt.Sprintf("days[%d].workouts: %d records exceed limit %d", n, excess, maxWorkoutsPerDay))
			break
		}
		var w Workout
		if err := decodeRecord(record, &w); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].workouts[%d]: %v", n, i, err))
			continue
		}
		if err := validateInterval(w.ID, w.StartAt, w.EndAt, date, zone, collected); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].workouts[%d]: %v", n, i, err))
			continue
		}
		if seen["workout:"+w.ID] || !detailText(w.Name, 256) || !metricPattern.MatchString(w.Activity) || !finiteNonnegative(w.DurationMinutes) || w.Items == nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].workouts[%d]: invalid workout", n, i))
			continue
		}
		items := make([]DetailMetric, 0, len(w.Items))
		seenMetrics := map[string]bool{}
		for j, item := range w.Items {
			if len(items) >= maxWorkoutMetrics {
				excess := len(w.Items) - j
				dropped += excess
				warnings = append(warnings, fmt.Sprintf("days[%d].workouts[%d].items: %d metrics exceed limit %d", n, i, excess, maxWorkoutMetrics))
				break
			}
			if !metricPattern.MatchString(item.Metric) || seenMetrics[item.Metric] || !detailText(item.Name, 256) || !detailText(item.Unit, 128) || !finiteNonnegative(item.Value) {
				dropped++
				warnings = append(warnings, fmt.Sprintf("days[%d].workouts[%d].items[%d]: invalid workout metric", n, i, j))
				continue
			}
			seenMetrics[item.Metric] = true
			items = append(items, item)
		}
		seen["workout:"+w.ID] = true
		w.Items = items
		kept = append(kept, w)
	}
	return kept, dropped, warnings
}

func filterIntake(records []json.RawMessage, n int, date string, zone *time.Location, collected time.Time) ([]IntakeRecord, int, []string) {
	kept := make([]IntakeRecord, 0, len(records))
	seen := map[string]bool{}
	dropped := 0
	warnings := []string{}
	units := map[string]string{"dietary_water": "mL", "dietary_caffeine": "mg"}
	for i, record := range records {
		if len(kept) >= maxIntakePerDay {
			excess := len(records) - i
			dropped += excess
			warnings = append(warnings, fmt.Sprintf("days[%d].intakeRecords: %d records exceed limit %d", n, excess, maxIntakePerDay))
			break
		}
		var r IntakeRecord
		if err := decodeRecord(record, &r); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].intakeRecords[%d]: %v", n, i, err))
			continue
		}
		if err := validateInterval(r.ID, r.StartAt, r.EndAt, date, zone, collected); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].intakeRecords[%d]: %v", n, i, err))
			continue
		}
		expectedUnit := units[r.Metric]
		if seen["intake:"+r.ID] || expectedUnit == "" || r.Unit != expectedUnit || !detailText(r.Name, 256) || !finiteNonnegative(r.Value) {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].intakeRecords[%d]: invalid intake record", n, i))
			continue
		}
		seen["intake:"+r.ID] = true
		kept = append(kept, r)
	}
	return kept, dropped, warnings
}

func filterMoods(records []json.RawMessage, n int, date string, zone *time.Location, collected time.Time) ([]Mood, int, []string) {
	kept := make([]Mood, 0, len(records))
	seen := map[string]bool{}
	dropped := 0
	warnings := []string{}
	for i, record := range records {
		if len(kept) >= maxMoodsPerDay {
			excess := len(records) - i
			dropped += excess
			warnings = append(warnings, fmt.Sprintf("days[%d].moods: %d records exceed limit %d", n, excess, maxMoodsPerDay))
			break
		}
		var mood Mood
		if err := decodeRecord(record, &mood); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].moods[%d]: %v", n, i, err))
			continue
		}
		if err := validateInterval(mood.ID, mood.StartAt, mood.EndAt, date, zone, collected); err != nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].moods[%d]: %v", n, i, err))
			continue
		}
		if seen["mood:"+mood.ID] || (mood.Kind != "daily_mood" && mood.Kind != "momentary_emotion") || math.IsNaN(mood.Valence) || mood.Valence < -1 || mood.Valence > 1 || mood.Labels == nil || mood.Associations == nil {
			dropped++
			warnings = append(warnings, fmt.Sprintf("days[%d].moods[%d]: invalid mood record", n, i))
			continue
		}
		labels, labelDrops := filterMoodList(mood.Labels, maxMoodLabels)
		associations, associationDrops := filterMoodList(mood.Associations, maxMoodAssociations)
		if labelDrops > 0 {
			dropped += labelDrops
			warnings = append(warnings, fmt.Sprintf("days[%d].moods[%d].labels: %d invalid labels dropped", n, i, labelDrops))
		}
		if associationDrops > 0 {
			dropped += associationDrops
			warnings = append(warnings, fmt.Sprintf("days[%d].moods[%d].associations: %d invalid associations dropped", n, i, associationDrops))
		}
		seen["mood:"+mood.ID] = true
		mood.Labels = labels
		mood.Associations = associations
		kept = append(kept, mood)
	}
	return kept, dropped, warnings
}

func filterMoodList(in []string, maximum int) ([]string, int) {
	kept := make([]string, 0, len(in))
	dropped := 0
	for i, label := range in {
		if len(kept) >= maximum {
			dropped += len(in) - i
			break
		}
		if !metricPattern.MatchString(label) {
			dropped++
			continue
		}
		kept = append(kept, label)
	}
	return kept, dropped
}

// AddDetails omits absent extensions so old snapshots keep their content hashes and response shape.
func (d DailySnapshot) AddDetails(data map[string]any) {
	if len(d.Medications) > 0 {
		data["medications"] = d.Medications
	}
	if len(d.Workouts) > 0 {
		data["workouts"] = d.Workouts
	}
	if len(d.IntakeRecords) > 0 {
		data["intakeRecords"] = d.IntakeRecords
	}
	if len(d.Moods) > 0 {
		data["moods"] = d.Moods
	}
}

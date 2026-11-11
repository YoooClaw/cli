package healthdata

import (
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

func detailText(s string, maximum int) bool { return strings.TrimSpace(s) != "" && len(s) <= maximum }
func finiteNonnegative(n float64) bool      { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 }
func optionalQuantity(n *float64) bool      { return n == nil || finiteNonnegative(*n) }

func detailInterval(id, start, end, date string, zone *time.Location, collected time.Time) (time.Duration, error) {
	a, ea := time.Parse(time.RFC3339Nano, start)
	b, eb := time.Parse(time.RFC3339Nano, end)
	if !detailText(id, 256) || ea != nil || eb != nil || b.Before(a) || a.After(collected) || a.In(zone).Format("2006-01-02") != date {
		return 0, fmt.Errorf("invalid health detail identity, date or interval")
	}
	return b.Sub(a), nil
}

func validateDetails(d DailySnapshot, zone *time.Location, collected time.Time) (int, error) {
	if len(d.Medications) > 128 || len(d.Workouts) > 128 || len(d.IntakeRecords) > 4096 || len(d.Moods) > 1024 {
		return 0, fmt.Errorf("health detail count exceeds limit")
	}
	count := len(d.Medications) + len(d.Workouts) + len(d.IntakeRecords) + len(d.Moods)
	seen := map[string]bool{}
	unique := func(kind, id string) bool {
		key := kind + ":" + id
		if !detailText(id, 256) || seen[key] {
			return false
		}
		seen[key] = true
		return true
	}
	doses := 0
	for _, m := range d.Medications {
		if !unique("medication", m.ID) || !detailText(m.Name, 2048) || !detailText(m.Form, 128) || (m.Nickname != nil && len(*m.Nickname) > 2048) || m.DoseEvents == nil {
			return 0, fmt.Errorf("invalid medication")
		}
		// Only today's list may include medications without an actual historical dose event.
		if len(m.DoseEvents) == 0 && d.Date != collected.In(zone).Format("2006-01-02") {
			return 0, fmt.Errorf("historical medication requires a dose event")
		}
		doses += len(m.DoseEvents)
		if doses > 4096 {
			return 0, fmt.Errorf("too many medication dose events")
		}
		for _, e := range m.DoseEvents {
			if _, err := detailInterval(e.ID, e.StartAt, e.EndAt, d.Date, zone, collected); err != nil {
				return 0, err
			}
			validStatus := map[string]bool{"not_interacted": true, "notification_not_sent": true, "snoozed": true, "taken": true, "skipped": true, "not_logged": true}
			if !unique("dose", e.ID) || (!validStatus[e.Status] && !strings.HasPrefix(e.Status, "unknown_")) || !detailText(e.Unit, 128) || !optionalQuantity(e.Quantity) || !optionalQuantity(e.ScheduledQuantity) {
				return 0, fmt.Errorf("invalid medication dose")
			}
			if e.ScheduleType == "scheduled" {
				if e.ScheduledAt == nil || e.ScheduledQuantity == nil {
					return 0, fmt.Errorf("scheduled dose requires schedule information")
				}
				if _, err := time.Parse(time.RFC3339Nano, *e.ScheduledAt); err != nil {
					return 0, fmt.Errorf("invalid scheduledAt")
				}
			} else if e.ScheduleType != "as_needed" || e.ScheduledAt != nil || e.ScheduledQuantity != nil {
				return 0, fmt.Errorf("invalid medication schedule type")
			}
		}
	}
	for _, w := range d.Workouts {
		_, err := detailInterval(w.ID, w.StartAt, w.EndAt, d.Date, zone, collected)
		if err != nil {
			return 0, err
		}
		if !unique("workout", w.ID) || !detailText(w.Name, 256) || !metricPattern.MatchString(w.Activity) || !finiteNonnegative(w.DurationMinutes) || w.Items == nil || len(w.Items) > 32 {
			return 0, fmt.Errorf("invalid workout")
		}
		metrics := map[string]bool{}
		for _, item := range w.Items {
			if !metricPattern.MatchString(item.Metric) || metrics[item.Metric] || !detailText(item.Name, 256) || !detailText(item.Unit, 128) || !finiteNonnegative(item.Value) {
				return 0, fmt.Errorf("invalid workout metric")
			}
			metrics[item.Metric] = true
		}
	}
	for _, intake := range d.IntakeRecords {
		if _, err := detailInterval(intake.ID, intake.StartAt, intake.EndAt, d.Date, zone, collected); err != nil {
			return 0, err
		}
		expectedUnit := map[string]string{"dietary_water": "mL", "dietary_caffeine": "mg"}[intake.Metric]
		if !unique("intake", intake.ID) || expectedUnit == "" || intake.Unit != expectedUnit || !detailText(intake.Name, 256) || !finiteNonnegative(intake.Value) {
			return 0, fmt.Errorf("invalid intake record")
		}
	}
	for _, mood := range d.Moods {
		if _, err := detailInterval(mood.ID, mood.StartAt, mood.EndAt, d.Date, zone, collected); err != nil {
			return 0, err
		}
		if !unique("mood", mood.ID) || (mood.Kind != "daily_mood" && mood.Kind != "momentary_emotion") || math.IsNaN(mood.Valence) || mood.Valence < -1 || mood.Valence > 1 || mood.Labels == nil || mood.Associations == nil || len(mood.Labels) > 64 || len(mood.Associations) > 32 {
			return 0, fmt.Errorf("invalid mood record")
		}
		for _, list := range [][]string{mood.Labels, mood.Associations} {
			for _, label := range list {
				if !metricPattern.MatchString(label) {
					return 0, fmt.Errorf("invalid mood label")
				}
			}
		}
	}
	return count, nil
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

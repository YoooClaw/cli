package healthdata

import "time"

// AddSummaryDetails keeps default asset/memory reads independent of per-day event volume.
// The CLI health-data get command continues to return the complete records.
func (d DailySnapshot) AddSummaryDetails(data map[string]any) {
	if len(d.Medications) > 0 {
		medications := make([]map[string]any, 0, len(d.Medications))
		for _, m := range d.Medications {
			summary := map[string]any{"id": m.ID, "name": m.Name, "form": m.Form, "isArchived": m.IsArchived, "hasSchedule": m.HasSchedule, "doseEventCount": len(m.DoseEvents)}
			if m.Nickname != nil {
				summary["nickname"] = *m.Nickname
			}
			var latest *MedicationDose
			var latestTime time.Time
			for i := range m.DoseEvents {
				date, _ := time.Parse(time.RFC3339Nano, m.DoseEvents[i].StartAt)
				if latest == nil || date.After(latestTime) {
					latest = &m.DoseEvents[i]
					latestTime = date
				}
			}
			if latest != nil {
				summary["latestDoseEvent"] = latest
			}
			medications = append(medications, summary)
		}
		data["medications"] = medications
	}
	if len(d.Workouts) > 0 {
		data["workouts"] = d.Workouts
	}
	if len(d.IntakeRecords) > 0 {
		data["intakeRecordCount"] = len(d.IntakeRecords)
		latest := d.IntakeRecords[0]
		for _, record := range d.IntakeRecords[1:] {
			a, _ := time.Parse(time.RFC3339Nano, record.StartAt)
			b, _ := time.Parse(time.RFC3339Nano, latest.StartAt)
			if a.After(b) {
				latest = record
			}
		}
		data["latestIntakeRecord"] = latest
	}
	if len(d.Moods) > 0 {
		data["moodCount"] = len(d.Moods)
		latest := d.Moods[0]
		for _, record := range d.Moods[1:] {
			a, _ := time.Parse(time.RFC3339Nano, record.StartAt)
			b, _ := time.Parse(time.RFC3339Nano, latest.StartAt)
			if a.After(b) {
				latest = record
			}
		}
		data["latestMood"] = latest
	}
	if len(d.Medications)+len(d.IntakeRecords)+len(d.Moods) > 0 {
		data["healthDetailsHint"] = "health-data get --date YYYY-MM-DD returns complete medication, intake and mood records"
	}
}

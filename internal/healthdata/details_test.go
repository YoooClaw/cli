package healthdata

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func detailsFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/health-details-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestOptionalDetailsRoundTripAndReceiptCounts(t *testing.T) {
	raw := detailsFixture(t)
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if batch.ItemCount != 6 {
		t.Fatal("receipt count", batch.ItemCount)
	}
	store := NewStorage(t.TempDir())
	if err := store.Append(batch); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(store.dir, "2026-11-05.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	day, _, err := ReadSnapshot(stored, "2026-11-05")
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Medications) != 1 || len(day.Workouts) != 1 || len(day.IntakeRecords) != 1 || len(day.Moods) != 1 {
		t.Fatalf("lost details: %+v", day)
	}
	if day.Medications[0].DoseEvents[0].Status != "taken" || day.Workouts[0].DurationMinutes != 40 || day.IntakeRecords[0].Value != 300 || day.Moods[0].Valence != 0.5 {
		t.Fatal("changed details")
	}
	first := append([]byte(nil), stored...)
	if err := store.Append(batch); err != nil {
		t.Fatal(err)
	}
	stored, _ = os.ReadFile(filepath.Join(store.dir, "2026-11-05.jsonl"))
	if !bytes.Equal(first, stored) {
		t.Fatal("repeated upload appended duplicates")
	}
}
func TestDetailsOnlyDayIsValidAndLegacyPayloadRemainsValid(t *testing.T) {
	batch, err := Prepare(detailsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	batch.daily.Days[0].Items = []json.RawMessage{}
	raw, _ := json.Marshal(batch.daily)
	next, err := Prepare(raw)
	if err != nil || next.ItemCount != 4 {
		t.Fatal(next.ItemCount, err)
	}
	if _, err = Prepare([]byte(dailyBody)); err != nil {
		t.Fatal("legacy v3 rejected", err)
	}
}
func TestDetailValidationRejectsMalformedRecordsBeforeStorage(t *testing.T) {
	changes := []func(*DailySnapshot){
		func(d *DailySnapshot) { d.Medications[0].DoseEvents[0].Status = "missing" },
		func(d *DailySnapshot) { d.Medications[0].DoseEvents[0].ScheduledAt = nil },
		func(d *DailySnapshot) { d.Medications[0].DoseEvents[0].Quantity = newFloat(-1) },
		func(d *DailySnapshot) {
			d.Medications[0].DoseEvents = append(d.Medications[0].DoseEvents, d.Medications[0].DoseEvents[0])
		},
		func(d *DailySnapshot) { d.Workouts[0].DurationMinutes = 46 },
		func(d *DailySnapshot) { d.Workouts[0].StartAt = "2026-11-04T09:00:00+08:00" },
		func(d *DailySnapshot) { d.IntakeRecords[0].Unit = "L" },
		func(d *DailySnapshot) { d.IntakeRecords = append(d.IntakeRecords, d.IntakeRecords[0]) },
		func(d *DailySnapshot) { d.Moods[0].Valence = 2 },
		func(d *DailySnapshot) { d.Moods[0].Labels = nil },
		func(d *DailySnapshot) { d.IntakeRecords = make([]IntakeRecord, 4097) },
	}
	for i, change := range changes {
		var b DailyBatch
		if err := json.Unmarshal(detailsFixture(t), &b); err != nil {
			t.Fatal(err)
		}
		change(&b.Days[0])
		raw, _ := json.Marshal(b)
		if _, err := Prepare(raw); err == nil {
			t.Fatalf("accepted malformed case %d", i)
		}
	}
	raw := strings.Replace(string(detailsFixture(t)), `"valence": 0.5`, `"valence": 0.5, "unexpected": true`, 1)
	if _, err := Prepare([]byte(raw)); err == nil {
		t.Fatal("accepted unknown nested field")
	}
}
func newFloat(v float64) *float64 { return &v }
func TestDetailReplacementDoesNotRetainOldEvents(t *testing.T) {
	batch, err := Prepare(detailsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStorage(t.TempDir())
	if err = store.Append(batch); err != nil {
		t.Fatal(err)
	}
	batch.daily.CollectedAt = "2026-11-05T19:00:00+08:00"
	batch.daily.Days[0].Medications = nil
	raw, _ := json.Marshal(batch.daily)
	next, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Append(next); err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(filepath.Join(store.dir, "2026-11-05.jsonl"))
	if bytes.Contains(stored, []byte("synthetic-medication")) {
		t.Fatal("old medication retained")
	}
	// Stale uploads must not restore an obsolete event.
	stale, err := Prepare(detailsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Append(stale); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(store.dir, "2026-11-05.jsonl"))
	if !bytes.Equal(stored, after) {
		t.Fatal("stale upload restored obsolete details")
	}
}

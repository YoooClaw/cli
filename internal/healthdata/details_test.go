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
	if _, err := store.Append(batch); err != nil {
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
	if _, err := store.Append(batch); err != nil {
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
		func(d *DailySnapshot) { d.Workouts[0].DurationMinutes = -1 },
		func(d *DailySnapshot) { d.Workouts[0].EndAt = "2026-11-05T00:00:00+08:00" },
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
		batch, dropped, warnings := drops(raw)
		if dropped == 0 || len(warnings) == 0 {
			t.Fatalf("malformed case %d was not dropped", i)
		}
		if batch.dayInfo[0].err != nil {
			t.Fatalf("malformed case %d took down the whole day: %v", i, batch.dayInfo[0].err)
		}
	}
	raw := strings.Replace(string(detailsFixture(t)), `"valence": 0.5`, `"valence": 0.5, "unexpected": true`, 1)
	batch, dropped, warnings := drops([]byte(raw))
	if dropped == 0 || len(warnings) == 0 || batch.dayInfo[0].err != nil {
		t.Fatalf("unknown nested field was not dropped: dropped=%d warnings=%v err=%v", dropped, warnings, batch.dayInfo[0].err)
	}
}
func newFloat(v float64) *float64 { return &v }
func TestDetailReplacementDoesNotRetainOldEvents(t *testing.T) {
	batch, err := Prepare(detailsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStorage(t.TempDir())
	if _, err = store.Append(batch); err != nil {
		t.Fatal(err)
	}
	batch.daily.CollectedAt = "2026-11-05T19:00:00+08:00"
	batch.daily.Days[0].Medications = nil
	raw, _ := json.Marshal(batch.daily)
	next, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(next); err != nil {
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
	if _, err = store.Append(stale); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(store.dir, "2026-11-05.jsonl"))
	if !bytes.Equal(stored, after) {
		t.Fatal("stale upload restored obsolete details")
	}
}

func TestDetailScalarsRequirePresenceAndAcceptZero(t *testing.T) {
	cases := []struct {
		section, field string
		nested         bool
		zero           any
	}{
		{"intakeRecords", "value", false, 0},
		{"moods", "valence", false, 0},
		{"workouts", "durationMinutes", false, 0},
		{"workouts", "value", true, 0},
		{"medications", "isArchived", false, false},
		{"medications", "hasSchedule", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.section+"/"+tc.field, func(t *testing.T) {
			for _, mode := range []string{"missing", "null", "zero"} {
				var b map[string]any
				if err := json.Unmarshal(detailsFixture(t), &b); err != nil {
					t.Fatal(err)
				}
				day := b["days"].([]any)[0].(map[string]any)
				record := day[tc.section].([]any)[0].(map[string]any)
				if tc.nested {
					record = record["items"].([]any)[0].(map[string]any)
				}
				switch mode {
				case "missing":
					delete(record, tc.field)
				case "null":
					record[tc.field] = nil
				default:
					record[tc.field] = tc.zero
				}
				raw, _ := json.Marshal(b)
				batch, dropped, warnings := drops(raw)
				if mode == "zero" {
					if dropped != 0 || batch.dayInfo[0].err != nil {
						t.Fatalf("zero value was not accepted: dropped=%d err=%v", dropped, batch.dayInfo[0].err)
					}
					continue
				}
				joined := strings.Join(warnings, "\n")
				if dropped == 0 || !strings.Contains(joined, tc.field) || batch.dayInfo[0].err != nil {
					t.Fatalf("%s: expected a named drop for %s, got dropped=%d warnings=%v err=%v", mode, tc.field, dropped, warnings, batch.dayInfo[0].err)
				}
			}
		})
	}
}

// Inner type errors must drop only the bad child element, not its parent.
func TestInnerTypeErrorsDropOnlyTheBadElement(t *testing.T) {
	cases := []struct {
		name   string
		warn   string
		change func(day map[string]any)
		check  func(t *testing.T, day DailySnapshot)
	}{
		{
			name: "dose event",
			warn: "doseEvents[1]",
			change: func(day map[string]any) {
				medication := day["medications"].([]any)[0].(map[string]any)
				medication["doseEvents"] = append(medication["doseEvents"].([]any), map[string]any{
					"id": "bad-dose", "startAt": "2026-11-05T08:00:00+08:00", "endAt": "2026-11-05T08:00:00+08:00",
					"status": "taken", "scheduleType": "as_needed", "quantity": "wrong-type", "unit": "count",
				})
			},
			check: func(t *testing.T, day DailySnapshot) {
				if len(day.Medications) != 1 || len(day.Medications[0].DoseEvents) != 1 {
					t.Fatalf("medication or its valid dose lost: %+v", day.Medications)
				}
			},
		},
		{
			name: "workout metric",
			warn: "items[1]",
			change: func(day map[string]any) {
				workout := day["workouts"].([]any)[0].(map[string]any)
				workout["items"] = append(workout["items"].([]any), map[string]any{
					"metric": "bad_metric", "name": "Bad", "value": "wrong-type", "unit": "bpm",
				})
			},
			check: func(t *testing.T, day DailySnapshot) {
				if len(day.Workouts) != 1 || len(day.Workouts[0].Items) != 1 {
					t.Fatalf("workout or its valid metric lost: %+v", day.Workouts)
				}
			},
		},
		{
			name: "mood label",
			warn: "labels",
			change: func(day map[string]any) {
				mood := day["moods"].([]any)[0].(map[string]any)
				mood["labels"] = append(mood["labels"].([]any), 123)
			},
			check: func(t *testing.T, day DailySnapshot) {
				if len(day.Moods) != 1 || len(day.Moods[0].Labels) != 1 || day.Moods[0].Labels[0] != "calm" {
					t.Fatalf("mood or its valid label lost: %+v", day.Moods)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(detailsFixture(t), &body); err != nil {
				t.Fatal(err)
			}
			tc.change(body["days"].([]any)[0].(map[string]any))
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			batch, dropped, warnings := drops(raw)
			if dropped != 1 || batch.dayInfo[0].err != nil {
				t.Fatalf("parent dropped instead of the bad element: dropped=%d err=%v", dropped, batch.dayInfo[0].err)
			}
			if !strings.Contains(strings.Join(warnings, "\n"), tc.warn) {
				t.Fatalf("warning does not name the bad element: %v", warnings)
			}
			tc.check(t, batch.daily.Days[0])
		})
	}
}

// A single malformed record must not cost its category or its day.
func TestDroppedRecordKeepsCategoryAndDay(t *testing.T) {
	var payload DailyBatch
	if err := json.Unmarshal(detailsFixture(t), &payload); err != nil {
		t.Fatal(err)
	}
	bad := payload.Days[0].Workouts[0]
	bad.ID = "synthetic-bad-workout"
	bad.DurationMinutes = -1
	payload.Days[0].Workouts = append(payload.Days[0].Workouts, bad)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if batch.dayInfo[0].err != nil || batch.dayInfo[0].dropped != 1 {
		t.Fatalf("day not isolated: err=%v dropped=%d", batch.dayInfo[0].err, batch.dayInfo[0].dropped)
	}
	store := NewStorage(t.TempDir())
	if _, err := store.Append(batch); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(store.dir, "2026-11-05.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("synthetic-bad-workout")) {
		t.Fatal("invalid workout was written")
	}
	day, _, err := ReadSnapshot(stored, "2026-11-05")
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Workouts) != 1 || len(day.Medications) != 1 || len(day.IntakeRecords) != 1 || len(day.Moods) != 1 {
		t.Fatalf("category lost with the bad record: %+v", day)
	}
}

// Providers may round duration independently from start/end timestamps.
// Preserve their reported value through both ingest and snapshot reads.
func TestWorkoutDurationPreservesProviderRounding(t *testing.T) {
	for _, duration := range []float64{5.5, 6, 7} {
		var payload DailyBatch
		if err := json.Unmarshal(detailsFixture(t), &payload); err != nil {
			t.Fatal(err)
		}
		payload.Provider = "honor"
		w := &payload.Days[0].Workouts[0]
		w.StartAt = "2026-11-05T14:10:57+08:00"
		w.EndAt = "2026-11-05T14:16:27+08:00"
		w.DurationMinutes = duration
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		batch, err := Prepare(raw)
		if err != nil {
			t.Fatalf("duration %v rejected: %v", duration, err)
		}
		store := NewStorage(t.TempDir())
		if _, err := store.Append(batch); err != nil {
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
		if got := day.Workouts[0].DurationMinutes; got != duration {
			t.Fatalf("provider duration changed: got %v, want %v", got, duration)
		}
	}
}

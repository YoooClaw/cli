package healthdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const dailyBody = `{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T16:00:00+08:00","days":[{"date":"2026-09-30","sleepSessions":[],"items":[{"metric":"steps","name":"Steps","value":31502,"unit":"steps"},{"metric":"active_energy","name":"Active Energy","value":420,"unit":"kcal"}]}]}`

func TestDailyUsesMeasurementDateAndReplacesRatherThanAdds(t *testing.T) {
	dir := t.TempDir()
	store := NewStorage(dir)
	batch, err := Prepare([]byte(dailyBody))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := store.Append(batch); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "2026-09-30.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})) != 3 {
		t.Fatalf("duplicate metrics or batch envelope: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"value":31502`)) || bytes.Contains(raw, []byte(`"days"`)) {
		t.Fatalf("not readable daily metrics: %s", raw)
	}
	// A later system correction replaces the complete visible day and removes obsolete metrics.
	corrected := strings.ReplaceAll(dailyBody, "31502", "31510")
	corrected = strings.ReplaceAll(corrected, "16:00:00", "17:00:00")
	next, _ := Prepare([]byte(corrected))
	if _, err := store.Append(next); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if bytes.Contains(got, []byte(`"value":31502`)) || !bytes.Contains(got, []byte(`"value":31510`)) {
		t.Fatalf("old total retained: %s", got)
	}
	// An older request finishing later cannot roll back a newer snapshot.
	if _, err := store.Append(batch); err != nil {
		t.Fatal(err)
	}
	later, _ := os.ReadFile(path)
	if !bytes.Equal(got, later) {
		t.Fatal("stale request overwrote newer result")
	}
}

// rejection returns the request-level error, or the first day-level validation
// error. Record-level failures are reported as drops, not errors.
func rejection(raw []byte) error {
	batch, err := Prepare(raw)
	if err != nil {
		return err
	}
	for _, info := range batch.dayInfo {
		if info.err != nil {
			return info.err
		}
	}
	return nil
}

// drops returns the per-day record drops recorded during Prepare.
func drops(raw []byte) (batch Batch, dropped int, warnings []string) {
	b, err := Prepare(raw)
	if err != nil {
		panic(err)
	}
	for _, info := range b.dayInfo {
		dropped += info.dropped
		warnings = append(warnings, info.warnings...)
	}
	return b, dropped, warnings
}

func TestDailyEnvelopeRejectsWholeRequest(t *testing.T) {
	bad := []string{
		strings.Replace(dailyBody, `"Asia/Shanghai"`, `"invalid/zone"`, 1),
		strings.Replace(dailyBody, `"2026-10-19T16:00:00+08:00"`, `"2026-10-19"`, 1),
		strings.Replace(dailyBody, `"schemaVersion":3`, `"schemaVersion":2`, 1),
		strings.Replace(dailyBody, `"provider":"healthkit"`, `"provider":" "`, 1),
		strings.Replace(dailyBody, `"provider":"healthkit"`, `"provider":"healthkit","unexpected":true`, 1),
	}
	for _, raw := range bad {
		if _, err := Prepare([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid request envelope: %s", raw)
		}
	}
}

func TestDailyInvalidDayIsRecordedWithoutRejectingOtherDays(t *testing.T) {
	// Day-level problems: no usable day identity or no content at all.
	for _, raw := range []string{
		strings.Replace(dailyBody, `"2026-09-30"`, `"../escape"`, 1),
		strings.Replace(dailyBody, `"2026-09-30"`, `"2026-02-30"`, 1),
		`{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T16:00:00+08:00","days":[{"date":"2026-09-30","sleepSessions":[],"items":[]}]}`,
	} {
		if err := rejection([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid day: %s", raw)
		}
	}
}

func TestDailyInvalidRecordsAreDroppedWithoutRejectingTheDay(t *testing.T) {
	// Record-level problems: the bad record disappears, the day survives.
	for _, raw := range []string{
		strings.Replace(dailyBody, `"value":31502`, `"value":1e999`, 1),
		strings.Replace(dailyBody, `"value":31502`, `"value":null`, 1),
		strings.Replace(dailyBody, `"value":31502`, `"value":"31502"`, 1),
		strings.Replace(dailyBody, `"steps"`, `"HKQuantityTypeIdentifierStepCount"`, 1),
		strings.Replace(dailyBody, `"active_energy"`, `"steps"`, 1),
		strings.Replace(dailyBody, `"unit":"steps"`, `"unit":"steps","device":{"large":"metadata"}`, 1),
	} {
		batch, dropped, warnings := drops([]byte(raw))
		if dropped != 1 || len(warnings) == 0 || batch.dayInfo[0].err != nil {
			t.Fatalf("record not isolated (dropped=%d warnings=%v err=%v): %s", dropped, warnings, batch.dayInfo[0].err, raw)
		}
		if batch.ItemCount != 1 {
			t.Fatalf("dropped record counted as ingested: %d", batch.ItemCount)
		}
	}
}

func TestInvalidDayDoesNotReserveItsDate(t *testing.T) {
	var payload DailyBatch
	if err := json.Unmarshal([]byte(dailyBody), &payload); err != nil {
		t.Fatal(err)
	}
	invalid := payload.Days[0]
	invalid.Items = []json.RawMessage{json.RawMessage(`{"metric":"steps","name":"","value":1,"unit":"steps"}`)}
	payload.Days = []DailySnapshot{invalid, payload.Days[0]}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if batch.dayInfo[0].err == nil || batch.dayInfo[1].err != nil {
		t.Fatalf("valid duplicate date was not let through: %+v", batch.dayInfo)
	}
	results, err := NewStorage(t.TempDir()).Append(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Reason != "invalid_snapshot" || !results[1].Applied {
		t.Fatalf("unexpected decisions: %+v", results)
	}
}

func TestDailyPartialBatchWritesValidDaysOnly(t *testing.T) {
	var payload DailyBatch
	if err := json.Unmarshal([]byte(dailyBody), &payload); err != nil {
		t.Fatal(err)
	}
	second := payload.Days[0]
	second.Date = "2026-10-01"
	second.Items = []json.RawMessage{json.RawMessage(`{"metric":"steps","name":"","value":1,"unit":"steps"}`)}
	payload.Days = append(payload.Days, second)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if batch.dayInfo[0].err != nil || batch.dayInfo[1].err == nil {
		t.Fatalf("unexpected day errors: %+v", batch.dayInfo)
	}
	if batch.ItemCount != 2 {
		t.Fatalf("rejected day counted in receipt: %d", batch.ItemCount)
	}
	dir := t.TempDir()
	results, err := NewStorage(dir).Append(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Applied || results[0].Reason != "" ||
		results[1].Applied || results[1].Reason != "invalid_snapshot" || results[1].Message == "" || results[1].Dropped != 1 {
		t.Fatalf("unexpected decisions: %+v", results)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-01.jsonl")); !os.IsNotExist(err) {
		t.Fatal("rejected day was written")
	}
	if stored, err := os.ReadFile(filepath.Join(dir, "2026-09-30.jsonl")); err != nil || len(stored) == 0 {
		t.Fatalf("valid day was not written: %v", err)
	}
}

func TestConcurrentDailySnapshotsAlwaysLeaveNewestCompleteResult(t *testing.T) {
	dir := t.TempDir()
	store := NewStorage(dir)
	var wg sync.WaitGroup
	for n := range 40 {
		wg.Go(func() {
			body := strings.Replace(dailyBody, "16:00:00", fmt.Sprintf("16:%02d:00", n), 1)
			body = strings.Replace(body, "31502", fmt.Sprint(n), 1)
			batch, err := Prepare([]byte(body))
			if err == nil {
				_, err = store.Append(batch)
			}
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(dir, "2026-09-30.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("torn snapshot: %s", raw)
	}
	var header dailyHeader
	if err := json.Unmarshal(lines[0], &header); err != nil || !strings.Contains(header.UpdatedAt, "16:39:00") {
		t.Fatalf("not newest: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"value":39`)) {
		t.Fatalf("mismatched header and data: %s", raw)
	}
}

func TestUnknownRootFormatIsNotArchivedOrOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-30.jsonl")
	raw := []byte("{\"schemaVersion\":4,\"date\":\"2026-09-30\"}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	batch, _ := Prepare([]byte(dailyBody))
	results, err := NewStorage(dir).Append(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Applied || results[0].Reason != "unreadable_snapshot" || results[0].Message == "" {
		t.Fatalf("unknown version not reported: %+v", results)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, raw) {
		t.Fatal("unknown data changed")
	}
	if _, err := Prepare([]byte(`{"schemaVersion":3,"items":[]}`)); err == nil {
		t.Fatal("unknown incoming schema treated as legacy")
	}
}

func TestUnreadableDayDoesNotBlockOtherDays(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "2026-10-01.jsonl")
	raw := []byte("{\"schemaVersion\":9,\"date\":\"2026-10-01\"}\n")
	if err := os.WriteFile(blocked, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var payload DailyBatch
	if err := json.Unmarshal([]byte(dailyBody), &payload); err != nil {
		t.Fatal(err)
	}
	second := payload.Days[0]
	second.Date = "2026-10-01"
	payload.Days = append(payload.Days, second)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare(body)
	if err != nil {
		t.Fatal(err)
	}
	results, err := NewStorage(dir).Append(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Applied || results[1].Reason != "unreadable_snapshot" || results[1].Applied {
		t.Fatalf("unexpected decisions: %+v", results)
	}
	if kept, _ := os.ReadFile(blocked); !bytes.Equal(kept, raw) {
		t.Fatal("unreadable day overwritten")
	}
	if stored, err := os.ReadFile(filepath.Join(dir, "2026-09-30.jsonl")); err != nil || len(stored) == 0 {
		t.Fatalf("other day not written: %v", err)
	}
}

func TestDailyUnknownRootFileAtOtherDateDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2020-01-01.jsonl")
	unknown := []byte("{\"schemaVersion\":4,\"date\":\"2020-01-01\"}\n")
	if err := os.WriteFile(path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare([]byte(dailyBody))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStorage(dir).Append(batch); err != nil {
		t.Fatalf("stray root file blocked ingest: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, unknown) {
		t.Fatal("unknown root file changed")
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "2026-09-30.jsonl")); err != nil || len(raw) == 0 {
		t.Fatalf("daily snapshot not written: %s %v", raw, err)
	}
}

func TestDailyEmptyRootFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-30.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	batch, _ := Prepare([]byte(dailyBody))
	if _, err := NewStorage(dir).Append(batch); err != nil {
		t.Fatalf("empty root file blocked ingest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte(`"value":31502`)) {
		t.Fatalf("empty file not replaced: %s %v", raw, err)
	}
}

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

const dailyBody = `{"schemaVersion":2,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T16:00:00+08:00","days":[{"date":"2026-09-30","items":[{"metric":"steps","name":"Steps","value":31502,"unit":"steps"},{"metric":"sleep_minutes","name":"Total Sleep Duration","value":420,"unit":"min"}]}]}`

func TestDailyUsesMeasurementDateAndReplacesRatherThanAdds(t *testing.T) {
	dir := t.TempDir()
	store := NewStorage(dir)
	batch, err := Prepare([]byte(dailyBody))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Append(batch); err != nil {
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
	if err := store.Append(next); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if bytes.Contains(got, []byte(`"value":31502`)) || !bytes.Contains(got, []byte(`"value":31510`)) {
		t.Fatalf("old total retained: %s", got)
	}
	// An older request finishing later cannot roll back a newer snapshot.
	if err := store.Append(batch); err != nil {
		t.Fatal(err)
	}
	later, _ := os.ReadFile(path)
	if !bytes.Equal(got, later) {
		t.Fatal("stale request overwrote newer result")
	}
}

func TestDailyValidatesEntireBatchBeforeChangingAnyFile(t *testing.T) {
	bad := []string{
		strings.Replace(dailyBody, `"2026-09-30"`, `"../escape"`, 1),
		strings.Replace(dailyBody, `"2026-09-30"`, `"2026-02-30"`, 1),
		strings.Replace(dailyBody, `"Asia/Shanghai"`, `"invalid/zone"`, 1),
		strings.Replace(dailyBody, `"2026-10-19T16:00:00+08:00"`, `"2026-10-19"`, 1),
		strings.Replace(dailyBody, `"value":31502`, `"value":1e999`, 1),
		strings.Replace(dailyBody, `"value":31502`, `"value":null`, 1),
		strings.Replace(dailyBody, `"value":31502`, `"value":"31502"`, 1),
		strings.Replace(dailyBody, `"steps"`, `"HKQuantityTypeIdentifierStepCount"`, 1),
		strings.Replace(dailyBody, `"sleep_minutes"`, `"steps"`, 1),
		strings.Replace(dailyBody, `"unit":"steps"`, `"unit":"steps","device":{"large":"metadata"}`, 1),
		`{"schemaVersion":2,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T16:00:00+08:00","days":[{"date":"2026-09-30","items":[]}]}`,
	}
	for _, raw := range bad {
		if _, err := Prepare([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid daily snapshot: %s", raw)
		}
	}
}

func TestDailyPreservesCollidingLegacyFileWithoutMixingFormats(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte("{\"schemaVersion\":1,\"items\":[]}\n")
	path := filepath.Join(dir, "2026-09-30.jsonl")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare([]byte(dailyBody))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStorage(dir).Append(batch); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "legacy", "imported", "2026-09-30.jsonl"))
	if err != nil || !bytes.Equal(backup, legacy) {
		t.Fatalf("legacy file not preserved: %v", err)
	}
	current, _ := os.ReadFile(path)
	if bytes.Contains(current, []byte(`"schemaVersion":1`)) {
		t.Fatal("raw batches mixed with daily metrics")
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
				err = store.Append(batch)
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

func TestDailyArchivesLegacyReceiptOutsideNewWindowButKeepsOldDailyHistory(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("{\"schemaVersion\":1,\"items\":[]}\n")
	if err := os.WriteFile(filepath.Join(dir, "2020-01-01.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	historical := []byte("{\"schemaVersion\":2,\"date\":\"2020-02-02\"}\n{\"metric\":\"steps\",\"name\":\"Steps\",\"value\":1,\"unit\":\"steps\"}\n")
	if err := os.WriteFile(filepath.Join(dir, "2020-02-02.jsonl"), historical, 0o600); err != nil {
		t.Fatal(err)
	}
	batch, _ := Prepare([]byte(dailyBody))
	if err := NewStorage(dir).Append(batch); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "legacy", "imported", "2020-01-01.jsonl"))
	if err != nil || !bytes.Equal(backup, raw) {
		t.Fatalf("legacy backup: %s %v", backup, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2020-01-01.jsonl")); !os.IsNotExist(err) {
		t.Fatal("legacy batches still in daily root")
	}
	kept, _ := os.ReadFile(filepath.Join(dir, "2020-02-02.jsonl"))
	if !bytes.Equal(kept, historical) {
		t.Fatal("out-of-window daily history changed")
	}
}

func TestDailyBackupFailureLeavesOriginalUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-30.jsonl")
	raw := []byte("{\"items\":[]}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	batch, _ := Prepare([]byte(dailyBody))
	if err := NewStorage(dir).Append(batch); err == nil {
		t.Fatal("backup failure was ignored")
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, raw) {
		t.Fatal("original modified before backup")
	}
}

func TestUnknownRootFormatIsNotArchivedOrOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-30.jsonl")
	raw := []byte("{\"schemaVersion\":3,\"date\":\"2026-09-30\"}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	batch, _ := Prepare([]byte(dailyBody))
	if err := NewStorage(dir).Append(batch); err == nil {
		t.Fatal("unknown version overwritten")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, raw) {
		t.Fatal("unknown data changed")
	}
	if _, err := Prepare([]byte(`{"schemaVersion":3,"items":[]}`)); err == nil {
		t.Fatal("unknown incoming schema treated as legacy")
	}
}

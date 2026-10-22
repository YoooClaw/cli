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
		strings.Replace(dailyBody, `"active_energy"`, `"steps"`, 1),
		strings.Replace(dailyBody, `"unit":"steps"`, `"unit":"steps","device":{"large":"metadata"}`, 1),
		`{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T16:00:00+08:00","days":[{"date":"2026-09-30","sleepSessions":[],"items":[]}]}`,
	}
	for _, raw := range bad {
		if _, err := Prepare([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid daily snapshot: %s", raw)
		}
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

func TestUnknownRootFormatIsNotArchivedOrOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-30.jsonl")
	raw := []byte("{\"schemaVersion\":4,\"date\":\"2026-09-30\"}\n")
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
	if err := NewStorage(dir).Append(batch); err != nil {
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
	if err := NewStorage(dir).Append(batch); err != nil {
		t.Fatalf("empty root file blocked ingest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte(`"value":31502`)) {
		t.Fatalf("empty file not replaced: %s %v", raw, err)
	}
}

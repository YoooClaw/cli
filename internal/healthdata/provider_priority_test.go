package healthdata

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func providerBatch(t *testing.T, provider, collected string, dates ...string) Batch {
	t.Helper()
	var payload DailyBatch
	if err := json.Unmarshal([]byte(dailyBody), &payload); err != nil {
		t.Fatal(err)
	}
	payload.Provider, payload.CollectedAt = provider, collected
	template := payload.Days[0]
	payload.Days = nil
	for _, date := range dates {
		day := template
		day.Date = date
		payload.Days = append(payload.Days, day)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestDailyProviderPriorityAndCollectionTime(t *testing.T) {
	// Vendor strings are not an allowlist: every non-healthkit provider shares
	// the same class, including an unknown future vendor.
	providers := []string{"healthkit", "honor", "huawei", "health_connect", "future_vendor"}
	for _, prior := range providers {
		for _, incoming := range providers {
			for _, stamp := range []string{"15:00:00", "16:00:00", "17:00:00"} {
				t.Run(prior+"/"+incoming+"/"+stamp, func(t *testing.T) {
					store := NewStorage(t.TempDir())
					first := providerBatch(t, prior, "2026-10-19T16:00:00+08:00", "2026-09-30")
					if _, err := store.Append(first); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(store.dir, "2026-09-30.jsonl")
					before, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					next := providerBatch(t, incoming, "2026-10-19T"+stamp+"+08:00", "2026-09-30")
					next.daily.Days[0].Items = []json.RawMessage{json.RawMessage(`{"metric":"steps","name":"Steps","value":123,"unit":"steps"}`)}
					results, err := store.Append(next)
					if err != nil || len(results) != 1 {
						t.Fatalf("results=%+v err=%v", results, err)
					}
					after, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					replace := false
					if prior == "healthkit" && incoming != "healthkit" {
						replace = false
					} else if prior != "healthkit" && incoming == "healthkit" {
						replace = true
					} else {
						replace = stamp >= "16:00:00"
					}
					if results[0].Date != "2026-09-30" || results[0].Applied != replace {
						t.Fatalf("unexpected decision: %+v", results)
					}
					wantReason := ""
					if !replace {
						wantReason = "stale_collected_at"
						if prior == "healthkit" && incoming != "healthkit" {
							wantReason = "apple_priority"
						}
					}
					if results[0].Reason != wantReason {
						t.Fatalf("unexpected reason: %+v", results)
					}
					if !replace {
						if !bytes.Equal(before, after) {
							t.Fatal("ignored upload changed snapshot")
						}
						return
					}
					day, header, err := ReadSnapshot(after, "2026-09-30")
					if err != nil {
						t.Fatal(err)
					}
					if header.Provider != incoming || len(day.Items) != 1 || !bytes.Contains(day.Items[0], []byte(`"value":123`)) {
						t.Fatalf("incoming complete snapshot was not selected: %s", after)
					}
					if _, err := store.Append(next); err != nil {
						t.Fatal(err)
					}
					repeated, _ := os.ReadFile(path)
					if !bytes.Equal(after, repeated) {
						t.Fatal("repeat changed snapshot")
					}
				})
			}
		}
	}
}

func TestApplePriorityIsPerDayWithinBatch(t *testing.T) {
	store := NewStorage(t.TempDir())
	if _, err := store.Append(providerBatch(t, "healthkit", "2026-10-19T15:00:00+08:00", "2026-09-30")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(providerBatch(t, "honor", "2026-10-19T16:00:00+08:00", "2026-09-30", "2026-10-01")); err != nil {
		t.Fatal(err)
	}
	for date, provider := range map[string]string{"2026-09-30": "healthkit", "2026-10-01": "honor"} {
		raw, err := os.ReadFile(filepath.Join(store.dir, date+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		_, header, err := ReadSnapshot(raw, date)
		if err != nil || header.Provider != provider {
			t.Fatalf("date %s: header=%+v err=%v", date, header, err)
		}
	}
}

func TestProviderNormalizationAndExistingAppleHeader(t *testing.T) {
	store := NewStorage(t.TempDir())
	for _, provider := range []string{" HealthKit ", "HEALTHKIT", "healthkit\t"} {
		b := providerBatch(t, provider, "2026-10-19T16:00:00+08:00", "2026-09-30")
		if b.daily.Provider != "healthkit" {
			t.Fatalf("provider not normalized: %q", b.daily.Provider)
		}
		if _, err := store.Append(b); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(store.dir, "2026-09-30.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise an existing snapshot written before provider normalization.
	raw = bytes.Replace(raw, []byte(`"provider":"healthkit"`), []byte(`"provider":" HealthKit "`), 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	b := providerBatch(t, " Future_Vendor ", "2026-10-19T17:00:00+08:00", "2026-09-30")
	if b.daily.Provider != "future_vendor" {
		t.Fatalf("provider=%q", b.daily.Provider)
	}
	results, err := store.Append(b)
	if err != nil || len(results) != 1 || results[0].Applied || results[0].Reason != "apple_priority" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("skipped request changed existing snapshot", err)
	}
}

func TestDailyWriteResultsSurviveLaterFailure(t *testing.T) {
	store := NewStorage(t.TempDir())
	if err := os.Mkdir(filepath.Join(store.dir, "2026-10-01.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	results, err := store.Append(providerBatch(t, "honor", "2026-10-19T16:00:00+08:00", "2026-09-30", "2026-10-01"))
	if err == nil || len(results) != 1 || results[0].Date != "2026-09-30" || !results[0].Applied {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, "2026-09-30.jsonl")); err != nil {
		t.Fatal(err)
	}
}

package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/healthdata"
)

func daily(t *testing.T, date, name string, value json.Number) []byte {
	t.Helper()
	header := map[string]any{"schemaVersion": 3, "date": date, "provider": "healthkit", "timeZone": "Asia/Shanghai", "updatedAt": "2026-10-22T09:00:00+08:00"}
	metric := map[string]any{"metric": "steps", "name": name, "value": value, "unit": "count"}
	return append(append(append(marshal(t, header), '\n'), marshal(t, metric)...), '\n')
}

func TestHealthDayPagingWholeSnapshotsAndNumbers(t *testing.T) {
	s := fixture(t)
	for d := 1; d <= 22; d++ {
		date := fmt.Sprintf("2026-10-%02d", d)
		put(t, s, "health-data", date+".jsonl", daily(t, date, "Steps", json.Number("9007199254740993")))
	}
	p := get(t, s, "health-data", url.Values{})
	if len(p.Items) != 7 || p.NextCursor == nil {
		t.Fatal("health default page size")
	}
	items := collect(t, s, "health-data", url.Values{"limit": {"14"}})
	if len(items) != 22 {
		t.Fatalf("got %d days", len(items))
	}
	for _, it := range items {
		if it.Data["date"] != *it.StorageDate || it.Data["provider"] != "healthkit" || !bytes.Contains(marshal(t, it), []byte("9007199254740993")) {
			t.Fatal("health data changed")
		}
		if len(it.Data["items"].([]json.RawMessage)) != 1 {
			t.Fatal("invented / split health metrics")
		}
	}
	date := "2026-10-22"
	id := p.Items[0].RecordID
	revision := p.Items[0].Data["revision"]
	replace(t, s.sources["health-data"].Dir+"/"+date+".jsonl", daily(t, date, "Steps", json.Number("10")))
	refreshed := get(t, s, "health-data", url.Values{"limit": {"1"}}).Items[0]
	if refreshed.RecordID != id || refreshed.Data["revision"] == revision {
		t.Fatal("daily refresh identity or revision")
	}
}

func TestHealthByteLimitsNeverSplitOrTrimMetrics(t *testing.T) {
	s := fixture(t)
	for _, date := range []string{"2026-10-21", "2026-10-22"} {
		put(t, s, "health-data", date+".jsonl", daily(t, date, strings.Repeat("n", 150000), json.Number("1")))
	}
	p := get(t, s, "health-data", url.Values{"limit": {"14"}})
	if len(p.Items) != 1 || p.NextCursor == nil || len(p.Items[0].TruncatedFields) != 0 {
		t.Fatal("whole-day byte cap")
	}
	items := collect(t, s, "health-data", url.Values{"limit": {"14"}})
	if len(items) != 2 {
		t.Fatal("deferred health day skipped")
	}
	s = fixture(t)
	put(t, s, "health-data", "2026-10-22.jsonl", daily(t, "2026-10-22", strings.Repeat("n", HealthItemLimit), json.Number("1")))
	path := put(t, s, "health-data", "2026-10-21.jsonl", nil)
	if e := os.Truncate(path, ScanLimit+1); e != nil {
		t.Fatal(e)
	}
	items = collect(t, s, "health-data", url.Values{"limit": {"14"}})
	if len(items) != 2 {
		t.Fatal("oversized days missing")
	}
	for _, it := range items {
		if it.Status != "oversized" || it.Data != nil || it.SourceBytes <= 0 {
			t.Fatal("partial health snapshot returned")
		}
	}
}

func TestHealthScanBudgetAndLegacyFileVisitBudget(t *testing.T) {
	s := fixture(t)
	// Two valid snapshots each take >2 MiB on disk, but fit after whitespace
	// removal. The second must be deferred without consuming its date.
	for _, date := range []string{"2026-10-21", "2026-10-22"} {
		raw := append(bytes.Repeat([]byte(" "), 2200000), daily(t, date, "Steps", json.Number("1"))...)
		put(t, s, "health-data", date+".jsonl", raw)
	}
	p := get(t, s, "health-data", url.Values{})
	if len(p.Items) != 1 || p.NextCursor == nil || warning(p, "SCAN_BUDGET_REACHED") != 1 {
		t.Fatal("health raw scan limit")
	}
	if len(collect(t, s, "health-data", url.Values{})) != 2 {
		t.Fatal("budget skipped day")
	}
}

func TestHealthCorruptionFailsAndReplacementRetry(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"schemaVersion":2,"items":[]}`), nil, []byte("{"), []byte(`{"schemaVersion":3,"date":"2026-10-22"}`),
		bytes.ReplaceAll(daily(t, "2026-10-22", "Steps", json.Number("1")), []byte(`"value":1`), []byte(`"value":"bad"`))} {
		s := fixture(t)
		put(t, s, "health-data", "2026-10-22.jsonl", raw)
		assertError(t, s, "health-data", url.Values{}, "SOURCE_READ_FAILED", 500)
	}
	for _, always := range []bool{false, true} {
		s := fixture(t)
		date := "2026-10-22"
		path := put(t, s, "health-data", date+".jsonl", daily(t, date, "Steps", json.Number("1")))
		calls := 0
		src := s.sources["health-data"]
		src.WithReadView = func(fn func() error) error {
			calls++
			if calls == 2 || always && calls == 4 {
				replace(t, path, daily(t, date, "Steps", json.Number(fmt.Sprint(calls))))
			}
			return fn()
		}
		s.sources["health-data"] = src
		if always {
			assertError(t, s, "health-data", url.Values{}, "SOURCE_CHANGED", 409)
		} else {
			p := get(t, s, "health-data", url.Values{})
			if calls != 4 || !bytes.Contains(marshal(t, p), []byte(`"value":2`)) {
				t.Fatal("retry failed to read replacement")
			}
		}
	}
}

func TestHealthRealStoreUpdatesReplaceInsteadOfAccumulate(t *testing.T) {
	s := fixture(t)
	store := healthdata.NewStorage(s.sources["health-data"].Dir)
	src := s.sources["health-data"]
	src.WithReadView = store.WithReadView
	s.sources["health-data"] = src
	for i := 1; i <= 2; i++ {
		raw := []byte(fmt.Sprintf(`{"schemaVersion":3,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-22T0%d:00:00+08:00","days":[{"date":"2026-10-21","sleepSessions":[],"items":[{"metric":"steps","name":"Steps","value":%d,"unit":"count"}]}]}`, i, i*10))
		b, e := healthdata.Prepare(raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = store.Append(b); e != nil {
			t.Fatal(e)
		}
	}
	p := get(t, s, "health-data", url.Values{})
	if len(p.Items) != 1 || !bytes.Contains(marshal(t, p), []byte(`"value":20`)) || bytes.Contains(marshal(t, p), []byte(`"value":30`)) {
		t.Fatal("daily values accumulated")
	}
}

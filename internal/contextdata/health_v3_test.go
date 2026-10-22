package contextdata

import (
	"encoding/json"
	"github.com/YoooClaw/cli/internal/healthdata"
	"github.com/YoooClaw/cli/internal/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sleepFixture(t *testing.T, p paths.Paths, stage, updated string) {
	t.Helper()
	source := "synthetic"
	stages := map[string]int{"core": 0, "deep": 0, "rem": 0, "unspecified": 0}
	stages[stage] = 60
	session := healthdata.SleepSession{SleepSummary: healthdata.SleepSummary{
		ID: strings.Repeat("a", 64), StartAt: "2026-10-21T23:30:00+08:00", EndAt: "2026-10-22T00:30:00+08:00", SleepMinutes: 60, StageMinutes: stages,
		Sources: []healthdata.SleepSource{{ID: source, Name: "Synthetic source"}},
	}, Segments: []healthdata.SleepSegment{{Stage: stage, StartAt: "2026-10-21T23:30:00+08:00", EndAt: "2026-10-22T00:30:00+08:00", Source: &source}}}
	fixture(t, filepath.Join(p.HealthData, "2026-10-22.jsonl"), map[string]any{"schemaVersion": 3, "date": "2026-10-22", "timeZone": "Asia/Shanghai", "provider": "test", "updatedAt": updated}, map[string]any{"type": "sleepSession", "session": session})
}

func TestHealthV3QueriesAndExactMemory(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "default")
	sleepFixture(t, p, "core", "2026-10-22T01:00:00+08:00")
	now, _ := time.Parse(time.RFC3339, "2026-10-22T12:00:00+08:00")
	for _, mode := range []string{"list", "get", "series"} {
		r, err := Query(p, "health-data", mode, "", Options{Date: "2026-10-22", Limit: 100, Metric: "steps"}, now)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		if mode == "get" && !strings.Contains(string(b), `"segments"`) {
			t.Fatal("missing full timeline")
		}
		if mode == "list" && (strings.Contains(string(b), `"segments"`) || !strings.Contains(string(b), `"segmentCount":1`)) {
			t.Fatal("unbounded list")
		}
		if mode == "series" && !strings.Contains(string(b), `"missing":true`) {
			t.Fatal("missing metric fabricated")
		}
	}
	a, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	token := a["token"].(string)
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), `"segments"`) {
		t.Fatal("unbounded memory batch")
	}
	sleepFixture(t, p, "deep", "2026-10-22T02:00:00+08:00")
	again, err := Next(p, "health-data", Options{All: true}, false)
	if err != nil || again["token"] != token {
		t.Fatal("pending snapshot changed", err)
	}
	if _, err = Commit(p, "health-data", token); err != nil {
		t.Fatal(err)
	}
	a, err = Next(p, "health-data", Options{All: true}, false)
	if err != nil || a["done"] != false {
		t.Fatal("lost timeline correction", err)
	}
	if _, err = Commit(p, "health-data", a["token"].(string)); err != nil {
		t.Fatal(err)
	}
	sleepFixture(t, p, "deep", "2026-10-22T03:00:00+08:00")
	a, err = Next(p, "health-data", Options{All: true}, false)
	if err != nil || a["done"] != true {
		t.Fatal("timestamp consumed again", err)
	}
	fixture(t, filepath.Join(p.HealthData, "2026-10-21.jsonl"), map[string]any{"schemaVersion": 2})
	a, err = Next(p, "health-data", Options{All: true}, true)
	if err == nil {
		t.Fatal("old schema accepted", err)
	}
	if err = os.Remove(filepath.Join(p.HealthData, "2026-10-21.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.HealthData, "2026-10-22.jsonl"), []byte("{\"schemaVersion\":3}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Next(p, "health-data", Options{All: true}, false); err == nil {
		t.Fatal("bad snapshot accepted")
	}
}

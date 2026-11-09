package healthdata

import (
	"encoding/json"
	"strings"
	"testing"
)

func semanticTypes(s FieldSemantics) string {
	var out []string
	for _, section := range s.Sections {
		out = append(out, section.Type)
	}
	return strings.Join(out, ",")
}
func TestSemanticsCatalogAndSelection(t *testing.T) {
	full, err := Semantics()
	if err != nil || full.FormatVersion != 1 || full.DataSchemaVersion != 3 || len(full.Revision) != 71 {
		t.Fatal(full, err)
	}
	seen := map[string]bool{}
	for _, s := range full.Sections {
		if seen[s.Type] || len(s.Rules) == 0 {
			t.Fatal(s)
		}
		seen[s.Type] = true
		if s.Type != "common" && len(s.MatchKeys) == 0 {
			t.Fatal(s)
		}
	}
	selected, err := Semantics("sleepSessions", "sleepSessions")
	if err != nil || semanticTypes(selected) != "common,sleepSessions" || selected.Revision != full.Revision {
		t.Fatal(selected, err)
	}
	if _, err = Semantics("typo"); err == nil {
		t.Fatal("unknown type accepted")
	}
	selected.Sections[0].Rules[0] = "changed by caller"
	fresh, _ := Semantics("common")
	if fresh.Sections[0].Rules[0] == "changed by caller" {
		t.Fatal("catalog mutable across calls")
	}
}
func TestSemanticsForFullSummaryAndPrevious(t *testing.T) {
	for _, data := range []map[string]any{
		{"intakeRecords": []any{map[string]any{"value": 1}}, "moods": []any{map[string]any{"valence": 0}}},
		{"intakeRecordCount": 1, "latestMood": map[string]any{"valence": 0}},
	} {
		got := SemanticsFor(data, map[string]any{"sleepSessions": []any{map[string]any{"isPartial": false}}})
		if semanticTypes(got) != "common,sleepSessions,intakeRecords,moods" {
			t.Fatal(got)
		}
	}
	if got := semanticTypes(SemanticsFor(map[string]any{"sleepSessions": []any{}, "items": []any{}})); got != "common" {
		t.Fatal(got)
	}
	if got := semanticTypes(SemanticsFor(map[string]any{"metric": "steps", "measurement": nil})); got != "common,items" {
		t.Fatal(got)
	}
	full, _ := Semantics("sleepSessions")
	raw, _ := json.Marshal(full)
	if !strings.Contains(string(raw), "isPartial") || !strings.Contains(string(raw), "false does not establish") {
		t.Fatal("sleep boundary meaning missing")
	}
}

func TestWorkoutOnlySnapshotIncludesMetricMeanings(t *testing.T) {
	// Workout-only days still return metric/name/value/unit inside workout items.
	got := SemanticsFor(map[string]any{"workouts": []Workout{{Items: []DetailMetric{{Metric: "heart_rate_average", Value: 80, Unit: "bpm"}}}}})
	if semanticTypes(got) != "common,items,workouts" {
		t.Fatalf("nested metric explanations missing: %s", semanticTypes(got))
	}
}

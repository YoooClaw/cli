package light

import "testing"

func TestOneShotDefaults(t *testing.T) {
	for _, mode := range validModes {
		input := map[string]any{"mode": mode, "duration_s": float64(8)}
		result := ValidateSegments(NormalizeOneShotSegments([]any{input}))
		if !result.Valid {
			t.Fatalf("%s: %+v", mode, result.Errors)
		}
		if _, mutated := input["brightness"]; mutated {
			t.Fatal("mutated caller")
		}
		if result.Segments[0]["brightness"] != float64(128) {
			t.Fatal("missing brightness default")
		}
	}
	for _, value := range []any{nil, "128", float64(256)} {
		result := ValidateSegments(NormalizeOneShotSegments([]any{map[string]any{"mode": "steady", "duration_s": float64(8), "brightness": value}}))
		if result.Valid {
			t.Fatalf("accepted invalid brightness %v", value)
		}
	}
	result := ValidateSegments(NormalizeOneShotSegments([]any{map[string]any{"mode": "steady", "duration_s": float64(8), "brightness": float64(0)}}))
	if !result.Valid || result.Segments[0]["brightness"] != float64(0) {
		t.Fatal("lost explicit zero")
	}
	display := DisplayOnlySegments()[0]
	if display["brightness"] != float64(0) || display["duration_s"] != float64(8) {
		t.Fatal(display)
	}
	for _, value := range []any{nil, []any{}, "bad", []any{map[string]any{"mode": "steady", "duration_s": float64(8), "color": nil}}} {
		if ValidateSegments(NormalizeOneShotSegments(value)).Valid {
			t.Fatal("accepted invalid segments")
		}
	}
}

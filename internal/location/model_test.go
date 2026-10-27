package location

import (
	"encoding/json"
	"testing"
)

func TestValidateCoordinates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		json string
		ok   bool
	}{
		{"zero", `{"longitude":0,"latitude":0}`, true},
		{"upper bounds", `{"longitude":180,"latitude":90}`, true},
		{"lower bounds", `{"longitude":-180,"latitude":-90}`, true},
		{"other fields unchecked", `{"longitude":120.5,"latitude":30.2,"horizontalAccuracy":-1,"collectedAt":false,"poiText":null}`, true},
		{"longitude too large", `{"longitude":180.01,"latitude":0}`, false},
		{"longitude too small", `{"longitude":-180.01,"latitude":0}`, false},
		{"latitude too large", `{"longitude":0,"latitude":90.01}`, false},
		{"latitude too small", `{"longitude":0,"latitude":-90.01}`, false},
		{"missing longitude", `{"latitude":0}`, false},
		{"missing latitude", `{"longitude":0}`, false},
		{"null longitude", `{"longitude":null,"latitude":0}`, false},
		{"string longitude", `{"longitude":"120","latitude":0}`, false},
		{"boolean latitude", `{"longitude":0,"latitude":true}`, false},
		{"overflow", `{"longitude":1e999,"latitude":0}`, false},
		{"null record", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var record Record
			if err := json.Unmarshal([]byte(tc.json), &record); err != nil {
				t.Fatal(err)
			}
			if err := record.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want valid=%v", err, tc.ok)
			}
		})
	}
}

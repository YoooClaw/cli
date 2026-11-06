package healthdata

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Required scalars must distinguish missing/null from a genuine zero or false.
// Aliases below retain the wire shape and the decoder's unknown-field checks.
func decodeDetail(raw []byte, target any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range required {
		value, exists := fields[key]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("health detail field %s is required and must not be null", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func (v *DetailMetric) UnmarshalJSON(raw []byte) error {
	type wire DetailMetric
	return decodeDetail(raw, (*wire)(v), "value")
}
func (v *Workout) UnmarshalJSON(raw []byte) error {
	type wire Workout
	return decodeDetail(raw, (*wire)(v), "durationMinutes")
}
func (v *IntakeRecord) UnmarshalJSON(raw []byte) error {
	type wire IntakeRecord
	return decodeDetail(raw, (*wire)(v), "value")
}
func (v *Mood) UnmarshalJSON(raw []byte) error {
	type wire Mood
	return decodeDetail(raw, (*wire)(v), "valence")
}
func (v *Medication) UnmarshalJSON(raw []byte) error {
	type wire Medication
	return decodeDetail(raw, (*wire)(v), "isArchived", "hasSchedule")
}

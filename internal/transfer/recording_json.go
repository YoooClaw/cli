package transfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Decode once, retaining precise numbers only inside extensible recording
// fields. Existing transfer fields keep their float64 representation.
func decodeTransferValue(raw []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	// JSON input comes from complete files/values, never an open stream.
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid trailing JSON")
	}
	return normalizeTransferNumbers(value), nil
}
func normalizeTransferNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		n, _ := v.Float64()
		return n
	case []any:
		for i := range v {
			v[i] = normalizeTransferNumbers(v[i])
		}
	case map[string]any:
		_, recording := v["id"]
		recording = recording && v["metadata"] != nil
		for key, child := range v {
			if recording {
				if key == "resultExtra" || key == "summaryData" {
					continue
				}
				if key == "metadata" {
					if meta, ok := child.(map[string]any); ok {
						if duration, exists := meta["duration_sec"]; exists {
							meta["duration_sec"] = normalizeTransferNumbers(duration)
						}
					}
					continue
				}
			}
			v[key] = normalizeTransferNumbers(child)
		}
	}
	return value
}
func (r *Record) UnmarshalJSON(raw []byte) error {
	type wire Record
	var value wire
	envelope := struct {
		*wire
		Data json.RawMessage `json:"data"`
	}{wire: &value}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	*r = Record(value)
	if len(envelope.Data) == 0 {
		return nil
	} // validateData reports INVALID_RECORD_DATA.
	data, err := decodeTransferValue(envelope.Data)
	if err != nil {
		return err
	}
	r.Data, _ = data.(map[string]any)
	return nil
}

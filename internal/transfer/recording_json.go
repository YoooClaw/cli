package transfer

import (
	"bytes"
	"encoding/json"
)

// Keep exact JSON numbers in extensible recording business fields. Other
// transfer fields retain their existing float64 representation and contracts.
func decodeTransferValue(raw []byte) (any, error) {
	var ordinary, precise any
	if err := json.Unmarshal(raw, &ordinary); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&precise); err != nil {
		return nil, err
	}
	preserveRecordingNumbers(ordinary, precise)
	return ordinary, nil
}
func preserveRecordingNumbers(ordinary, precise any) {
	switch value := ordinary.(type) {
	case []any:
		other, _ := precise.([]any)
		for i := range value {
			preserveRecordingNumbers(value[i], other[i])
		}
	case map[string]any:
		other, _ := precise.(map[string]any)
		// Both index entries and portable recording data have id + metadata.
		if _, hasID := value["id"]; hasID && value["metadata"] != nil {
			for _, key := range []string{"resultExtra", "summaryData"} {
				if v, ok := other[key]; ok {
					value[key] = v
				}
			}
			if meta, ok := value["metadata"].(map[string]any); ok {
				exact, _ := other["metadata"].(map[string]any)
				for key, v := range exact {
					if key != "duration_sec" {
						meta[key] = v
					}
				}
			}
			return
		}
		for key, v := range value {
			preserveRecordingNumbers(v, other[key])
		}
	}
}

func (r *Record) UnmarshalJSON(raw []byte) error {
	type wire Record
	var v wire
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*r = Record(v)
	if r.Type == TypeRecordings {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		data, err := decodeTransferValue(fields["data"])
		if err != nil {
			return err
		}
		r.Data, _ = data.(map[string]any)
	}
	return nil
}

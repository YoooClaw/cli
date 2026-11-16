package recording

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// Keep App metadata extensions without requiring a CLI release for every field.
// Typed fields remain authoritative; extensions cannot override them.
func jsonFieldNames(value any) []string {
	t := reflect.TypeOf(value)
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}
	return names
}
func decodeMetadataExtra(data []byte, typed any, names []string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, typed); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(data, &extra); err != nil {
		return nil, err
	}
	for _, name := range names {
		delete(extra, name)
	}
	return extra, nil
}
func encodeMetadataExtra(typed any, extra map[string]json.RawMessage, names []string) ([]byte, error) {
	raw, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return raw, nil
	}
	out := mergeMetadataExtra(nil, extra)
	for _, name := range names {
		delete(out, name)
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(raw, &known); err != nil {
		return nil, err
	}
	for name, value := range known {
		out[name] = value
	}
	return json.Marshal(out)
}
func mergeMetadataExtra(current, incoming map[string]json.RawMessage) map[string]json.RawMessage {
	if len(current)+len(incoming) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(current)+len(incoming))
	for key, value := range current {
		out[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range incoming {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

type metadataJSON Metadata

var metadataJSONNames = jsonFieldNames(metadataJSON{})

func (m *Metadata) UnmarshalJSON(data []byte) error {
	var typed metadataJSON
	extra, err := decodeMetadataExtra(data, &typed, metadataJSONNames)
	if err != nil {
		return err
	}
	*m = Metadata(typed)
	m.Extra = extra
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["location"]; ok {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&m.Location); err != nil {
			return err
		}
	}
	return nil
}
func (m Metadata) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(metadataJSON(m), m.Extra, metadataJSONNames)
}

type exportRecordingJSON ExportRecording

var exportRecordingJSONNames = jsonFieldNames(exportRecordingJSON{})

func (m *ExportRecording) UnmarshalJSON(data []byte) error {
	var typed exportRecordingJSON
	extra, err := decodeMetadataExtra(data, &typed, exportRecordingJSONNames)
	if err != nil {
		return err
	}
	*m = ExportRecording(typed)
	m.Extra = extra
	return nil
}
func (m ExportRecording) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(exportRecordingJSON(m), m.Extra, exportRecordingJSONNames)
}

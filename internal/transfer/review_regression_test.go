package transfer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecordingImportStripsRuntimeMetadata(t *testing.T) {
	meta := map[string]any{"name": "test", "created_at": "2026-11-16T00:00:00Z", "future": map[string]any{"order": []any{2, 1}}}
	for _, k := range recordingRuntimeMetadataFields {
		meta[k] = "untrusted"
	}
	data := map[string]any{"id": "test", "metadata": meta}
	entry, err := materialize(context.Background(), Record{Type: TypeRecordings, Data: data}, t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clean := entry["metadata"].(map[string]any)
	for _, k := range recordingRuntimeMetadataFields {
		if _, ok := clean[k]; ok {
			t.Fatalf("runtime field retained: %s", k)
		}
	}
	if clean["future"] == nil || meta["oss_audio_url"] == nil {
		t.Fatal("business field lost or source mutated")
	}
}
func TestRecordingLegacyMetadataAndMissingData(t *testing.T) {
	data := map[string]any{"id": "test", "metadata": map[string]any{"name": "test", "created_at": "2026-11-16T00:00:00Z", "duration_sec": "abc"}}
	if err := validateData(TypeRecordings, data); err != nil {
		t.Fatalf("legacy metadata must not introduce a package-wide rejection: %v", err)
	}
	var r Record
	if err := json.Unmarshal([]byte(`{"type":"recordings"}`), &r); err != nil {
		t.Fatal(err)
	}
	if err := validateData(TypeRecordings, r.Data); err == nil || !strings.Contains(err.Error(), "INVALID_RECORD_DATA") {
		t.Fatalf("missing data: %v", err)
	}
}

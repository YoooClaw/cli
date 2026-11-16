package recording

import (
	"encoding/json"
	"testing"
)

func TestMetadataExtensionsPersistAndExport(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	var params ResultWriteParams
	if err := json.Unmarshal([]byte(`{"recordingId":"rec_export","recording":{"future":{"nested":[1,true,null]},"large_id":9007199254740993},"summary":{"markdown":"summary"}}`), &params); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	params.Recording = &Metadata{Name: "Renamed"}
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStorage(storage.dir, testLogger{t})
	if err := reloaded.Init(); err != nil {
		t.Fatal(err)
	}
	entry, ok := reloaded.FindByID("rec_export")
	if !ok {
		t.Fatal("missing")
	}
	if string(entry.Metadata.Extra["large_id"]) != "9007199254740993" {
		t.Fatalf("lost precision: %s", entry.Metadata.Extra["large_id"])
	}
	if len(entry.Metadata.Extra["future"]) == 0 {
		t.Fatal("lost nested extension")
	}
	start := startExport(t, service, cloud, "rec_export", "extension-request", "local")
	status := waitExport(t, service, cloud, "rec_export", start.ExportID)
	if status.Result == nil {
		t.Fatalf("export failed: %+v", status)
	}
	raw, err := json.Marshal(status.Result.WritePayload.Recording)
	if err != nil {
		t.Fatal(err)
	}
	var exported ExportRecording
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if string(exported.Extra["large_id"]) != "9007199254740993" {
		t.Fatalf("lost export metadata: %s", raw)
	}
	// Known fields remain authoritative even if an internal caller supplies a conflicting extension.
	exported.Extra["name"] = json.RawMessage(`"spoofed"`)
	raw, err = json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if decoded["name"] != "Renamed" {
		t.Fatal("extension overrode typed field")
	}
}

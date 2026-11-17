package recording

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultExtensionsStorageAndExport(t *testing.T) {
	storage, _ := seedExportPayloadRecording(t, nil, nil)
	var params ResultWriteParams
	input := `{"recordingId":"rec_payload","future":[3,1,2,[false,null,9007199254740993]],"recording":{"name":"产品评审","markers":[{"index":2,"timestamp_ms":12,"future":["b","a"]}],"future":{"n":9007199254740993}},"transcript":{"text":"hello","future":[[3,2,1],null,9007199254740993],"source":{"provider":"app","future":{"list":[2,1]}},"segments":[{"text":"second","speakerName":"B","extra":[2,1]},{"text":"first","speakerName":"A"},{"text":"","extra":[3,2,1]}]},"summary":{"markdown":"# Summary\n\nhello","structured":{"items":[2,1,null]},"future":{"n":9007199254740993}}}`
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	reload := NewStorage(storage.dir, testLogger{t})
	if err := reload.Init(); err != nil {
		t.Fatal(err)
	}
	entry, _ := reload.FindByID("rec_payload")
	if entry.SummaryData == nil || entry.SummaryData.Markdown != "" {
		t.Fatal("summary Markdown duplicated")
	}
	docRaw, err := os.ReadFile(filepath.Join(storage.dir, entry.TranscriptDataFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc TranscriptDocument
	if err := json.Unmarshal(docRaw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Raw != nil {
		t.Fatal("result.write duplicated the full transcript")
	}
	if compactSegmentJSON(t, doc.ResultExtra["future"]) != `[[3,2,1],null,9007199254740993]` {
		t.Fatal("transcript extras lost")
	}
	source, err := prepareExportSource(reload, "rec_payload", true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(source.payload)
	if err != nil {
		t.Fatal(err)
	}
	var exported ResultWriteParams
	if err := json.Unmarshal(encoded, &exported); err != nil {
		t.Fatal(err)
	}
	if compactSegmentJSON(t, exported.Extra["future"]) != `[3,1,2,[false,null,9007199254740993]]` {
		t.Fatal("top-level extras changed")
	}
	if compactSegmentJSON(t, exported.Transcript.Extra["future"]) != `[[3,2,1],null,9007199254740993]` {
		t.Fatal("transcript extras changed")
	}
	if compactSegmentJSON(t, exported.Transcript.Source.Extra["future"]) != `{"list":[2,1]}` {
		t.Fatal("source extras changed")
	}
	if len(exported.Transcript.Segments) != 3 || compactSegmentJSON(t, exported.Transcript.Segments[2].Extra["extra"]) != `[3,2,1]` {
		t.Fatal("empty segment lost")
	}
	if exported.Transcript.Segments[0].Text != "second" || exported.Transcript.Segments[1].Text != "first" {
		t.Fatal("segment order changed")
	}
	if compactSegmentJSON(t, exported.Recording.Markers[0].Extra["future"]) != `["b","a"]` {
		t.Fatal("marker extensions lost")
	}
	if compactSegmentJSON(t, exported.Summary.Extra["future"]) != `{"n":9007199254740993}` {
		t.Fatal("summary extras lost")
	}
	var update ResultWriteParams
	if err := json.Unmarshal([]byte(`{"recordingId":"rec_payload","summary":{"markdown":"updated"},"future":[]}`), &update); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleRecordingResultWrite(update, reload, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	next, err := prepareExportSource(reload, "rec_payload", false)
	if err != nil {
		t.Fatal(err)
	}
	if next.sourceVersion == source.sourceVersion {
		t.Fatal("version not updated")
	}
	if string(next.payload.Extra["future"]) != `[]` {
		t.Fatal("array not replaced")
	}
	if !strings.Contains(next.payload.Summary.Markdown, "updated") || len(next.payload.Summary.Extra) != 0 {
		t.Fatal("new summary retained stale extensions or lost Markdown")
	}
	files, err := filepath.Glob(filepath.Join(storage.dir, "transcript-data", "*.app-result.json"))
	if err != nil || len(files) > 0 {
		t.Fatal("whole-request artifact created")
	}
}

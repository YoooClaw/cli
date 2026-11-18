package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
)

func TestCaptureLegacyGatewayReadAndExport(t *testing.T) {
	srv, ts := newTestServer(t, "")
	withRecordings(t, srv)
	root := withCaptureRecordings(t, srv)
	history := filepath.Join(srv.ctx.Paths.Voice, "recordings-jsonl")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"old.m4a":  "audio",
		"old.json": `{"transcripts":[{"sentences":[{"text":"历史转写","begin_time":100,"end_time":3000,"speaker_id":0}]}]}`,
		"old.md":   "历史总结",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	row := `{"id":"legacy","title":"历史录音","recorded_at":"2026-11-10T10:00:00+08:00","duration_ms":3000,"audio_rel_path":"recordings/old.m4a","transcript_rel_path":"recordings/old.json","summary_rel_path":"recordings/old.md"}` + "\n"
	if err := os.WriteFile(filepath.Join(history, "2026-11-10.jsonl"), []byte(row), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) map[string]any {
		return gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/"+method, "phone-a", body))
	}
	if data := call("recordings.list", `{}`); data["total"] != float64(0) {
		t.Fatalf("default: %+v", data)
	}
	for _, source := range []string{"capture_app", "all"} {
		data := call("recordings.list", `{"sourceType":"`+source+`"}`)
		if data["total"] != float64(1) {
			t.Fatalf("list: %+v", data)
		}
		data = call("recordings.status", `{"sourceType":"`+source+`","recordingId":"legacy"}`)
		if data["recordingId"] != "legacy" || data["sourceType"] != "capture_app" {
			t.Fatalf("detail: %+v", data)
		}
	}
	if _, found, err := srv.captureRecordingStorage.LookupByID("legacy"); err != nil || found {
		t.Fatalf("read migrated: %v %v", found, err)
	}
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(string) (recording.ExportCloud, error) { return cloud, nil }
	start := call("recordings.export.start", `{"sourceType":"capture_app","recordingId":"legacy","requestId":"legacy-request"}`)
	id := asString(start["exportId"])
	status := waitGatewayExport(t, ts.URL, "phone-a", "legacy", id, "capture_app")
	if status["state"] != "ready" {
		t.Fatalf("export: %+v", status)
	}
	raw, _ := json.Marshal(status["result"])
	for _, want := range []string{"历史转写", "历史总结", "speakerId", "startMs"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s: %s", want, raw)
		}
	}
	// A fresh storage instance must retain the request and never upload twice.
	withCaptureRecordings(t, srv)
	replay := call("recordings.export.start", `{"sourceType":"capture_app","recordingId":"legacy","requestId":"legacy-request"}`)
	if replay["exportId"] != id || cloud.uploads != 1 {
		t.Fatalf("replay: %+v uploads=%d", replay, cloud.uploads)
	}
	if data := call("recordings.list", `{"sourceType":"capture_app"}`); data["total"] != float64(1) {
		t.Fatalf("duplicate: %+v", data)
	}
	// Deleting an adopted recording must not resurrect its immutable daily row.
	if _, err := srv.captureRecordingStorage.Delete("legacy", false); err != nil {
		t.Fatal(err)
	}
	if data := call("recordings.list", `{"sourceType":"capture_app"}`); data["total"] != float64(0) {
		t.Fatalf("resurrected: %+v", data)
	}
	resp := asClient(t, "POST", ts.URL+"/gateway/recordings.status", "phone-a", `{"sourceType":"capture_app","recordingId":"legacy"}`)
	if errorCode(resp) != "NOT_FOUND" {
		t.Fatalf("deleted detail: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(root, "old.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy transcript remains: %v", err)
	}
	// An explicit subsequent write can recreate that UUID.
	if _, err := srv.captureRecordingStorage.Ingest("legacy", recording.Metadata{Name: "new"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := srv.captureRecordingStorage.LookupSourceByID(t.Context(), "legacy"); err != nil || !found {
		t.Fatalf("recreate: %v %v", found, err)
	}

}

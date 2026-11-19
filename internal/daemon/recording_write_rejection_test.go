package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
)

func TestRecordingWriteEarlyRejectionLogs(t *testing.T) {
	for _, tc := range []struct{ name, body, reason, code string }{
		{"syntax", `{"recordingId":`, "invalid_json", "INVALID_PARAMS"},
		{"type", `{"recordingId":"r","transcript":{"segments":[{"speakerId":"secret-value"}]}}`, "json_type_mismatch", "INVALID_PARAMS"},
		{"validation", `{"recordingId":"v"}`, "transcript or summary is required", "INVALID_PARAMS"},
		{"missing", `{}`, "recordingId_required", "INVALID_PARAMS"},
		{"unsafe", `{"recordingId":"../secret-value"}`, "recordingId_unsafe", "INVALID_PARAMS"},
		{"source", `{"recordingId":"r","sourceType":"bad"}`, "sourceType_invalid", "INVALID_PARAMS"},
		{"conflict", `{"recordingId":"r","sourceType":"capture_app","transcript":{"text":"test"}}`, "recordingId_exists_in_other_source", "SOURCE_CONFLICT"},
		{"hidden", `{"recordingId":"hidden","sourceType":"capture_app","transcript":{"text":"test"}}`, "recording_not_visible_to_caller", "NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ts := newTestServer(t, "")
			store := withRecordings(t, s)
			withCaptureRecordings(t, s)
			if _, err := store.Ingest("r", recording.Metadata{Name: "test"}, "phone-a"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.captureRecordingStorage.Ingest("hidden", recording.Metadata{Name: "test"}, "phone-b"); err != nil {
				t.Fatal(err)
			}
			resp := asClient(t, "POST", ts.URL+"/gateway/recordings.result.write", "phone-a", tc.body)
			if errorCode(resp) != tc.code {
				t.Fatalf("response=%v", resp)
			}
			raw, err := os.ReadFile(s.logger.logFile)
			if err != nil {
				t.Fatal(err)
			}
			logs := string(raw)
			if !strings.Contains(logs, "recording.write.rejected") || !strings.Contains(logs, tc.reason) || !strings.Contains(logs, tc.code) {
				t.Fatalf("missing rejection: %s", logs)
			}
			if strings.Contains(logs, "secret-value") {
				t.Fatalf("leaked input: %s", logs)
			}
		})
	}
}
func TestRecordingDecodeReasonDoesNotExposeValues(t *testing.T) {
	var target struct{ Count int }
	err := json.Unmarshal([]byte(`{"Count":9999999999999999999999999999999999}`), &target)
	got := recordingDecodeReason(err)
	if strings.Contains(got, "999999") || !strings.Contains(got, "json_type_mismatch") {
		t.Fatal(got)
	}
}

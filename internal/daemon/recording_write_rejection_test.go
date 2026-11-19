package daemon

import (
	"encoding/json"
	"errors"
	"github.com/YoooClaw/cli/internal/relay"
	"net/http"
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

func TestRecordingProtocolFieldDiagnostics(t *testing.T) {
	for _, field := range []string{"file_size_display", "transfer_status", "oss_audio_url", "oss_srt_url"} {
		var params recording.ResultWriteParams
		err := json.Unmarshal([]byte(`{"recording":{"`+field+`":123}}`), &params)
		if err == nil {
			t.Fatalf("%s: expected type error", field)
		}
		if got := recordingDecodeReason(err); !strings.Contains(got, "field=recording."+field) {
			t.Fatal(got)
		}
	}
	var params recording.ResultWriteParams
	err := json.Unmarshal([]byte(`{"recording":{"markers":[{"index":"private-value"}]}}`), &params)
	if err == nil {
		t.Fatal("expected marker type error")
	}
	if got := recordingDecodeReason(err); !strings.Contains(got, "field=recording.markers.index") || strings.Contains(got, "private-value") {
		t.Fatal(got)
	}
	var unknown struct {
		Private int `json:"private-field"`
	}
	err = json.Unmarshal([]byte(`{"private-field":"private-value"}`), &unknown)
	if got := recordingDecodeReason(err); !strings.Contains(got, "field=[redacted]") || strings.Contains(got, "private-field") || strings.Contains(got, "private-value") {
		t.Fatal(got)
	}
}

func TestRecordingLogIdentifiers(t *testing.T) {
	for _, id := range []string{"recording.v1", strings.Repeat("a", 200), "录音一"} {
		if got := safeRecordingLogID("  " + id + "  "); got != id {
			t.Fatalf("id=%q got=%q", id, got)
		}
	}
	for _, id := range []string{"../private", strings.Repeat("a", 201), "ock_private", "ock-private"} {
		if got := safeRecordingLogID(id); got != "[redacted]" {
			t.Fatalf("id=%q got=%q", id, got)
		}
	}
}

func TestRecordingWriteLogsEscapeInjection(t *testing.T) {
	s, ts := newTestServer(t, "")
	withRecordings(t, s)
	id := "abc\nforged-line-here"
	body, _ := json.Marshal(map[string]any{"recordingId": id, "transcript": map[string]any{"text": "test"}})
	resp := asClient(t, "POST", ts.URL+"/gateway/recordings.result.write", "phone-a", string(body))
	gatewayData(t, resp) // Logging changes must not reject previously accepted IDs.
	s.logRecordingWriteFailure(id, "relay\nforged-relay", errors.New("write failed\nforged-error token=secret-value https://example.test/audio?Signature=private"))
	raw, err := os.ReadFile(s.logger.logFile)
	if err != nil {
		t.Fatal(err)
	}
	logs := string(raw)
	for _, fragment := range []string{"\nforged-", "secret-value", "Signature=private"} {
		if strings.Contains(logs, fragment) {
			t.Fatalf("unsafe log %q: %s", fragment, logs)
		}
	}
	for _, fragment := range []string{"写入受理", "转录文本已写入", "[recording-status]", "写入失败", `abc\nforged-line-here`} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("missing %q: %s", fragment, logs)
		}
	}
}

func TestRecordingRejectionCorrelationAndNormalization(t *testing.T) {
	s, ts := newTestServer(t, "")
	withRecordings(t, s)
	req, err := http.NewRequest("POST", ts.URL+"/gateway/recordings.result.write", strings.NewReader(`{"recordingId":"  recording.v1  ","sourceType":" mobile_app "}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(relay.InternalRequestIDHeader, "relay-review-206")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if errorCode(result) != "INVALID_PARAMS" {
		t.Fatal(result)
	}
	raw, err := os.ReadFile(s.logger.logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"relay":"relay-review-206"`, `"recordingId":"recording.v1"`, `"sourceType":"mobile_app"`} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("missing %s: %s", fragment, raw)
		}
	}
}

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/YoooClaw/cli/internal/diagnostics"
	"github.com/YoooClaw/cli/internal/relay"
)

// Observe only this handler's small error envelope, never request content or
// error.message (which can contain caller-controlled IDs).
type recordingWriteResponse struct {
	http.ResponseWriter
	code string
}

func (w *recordingWriteResponse) Write(b []byte) (int, error) {
	if len(b) <= 8192 {
		var envelope struct{ Error struct{ Code string } }
		if json.Unmarshal(b, &envelope) == nil {
			w.code = envelope.Error.Code
		}
	}
	return w.ResponseWriter.Write(b)
}

var recordingLogID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func safeRecordingLogID(s string) string {
	if s == "" {
		return ""
	}
	if !recordingLogID.MatchString(s) || strings.HasPrefix(s, "ock_") || strings.HasPrefix(s, "ock-") {
		return "[redacted]"
	}
	return s
}
func recordingDecodeReason(err error) string {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Sprintf("invalid_json offset=%d", syntax.Offset)
	}
	var typed *json.UnmarshalTypeError
	if errors.As(err, &typed) {
		// Field names can originate in arbitrary maps; only retain known protocol names.
		names := map[string]bool{}
		for _, name := range strings.Fields("recordingId sourceType ossUrl ossTaskId durationMillis recording transcript summary segments text startMs endMs speakerId speakerName name created_at duration_sec duration_display recording_type markers timestamp_ms location title category brief markdown generatedAt source provider taskId requestId status format structured") {
			names[name] = true
		}
		field := typed.Field
		for _, part := range strings.Split(field, ".") {
			if !names[part] {
				field = "[redacted]"
				break
			}
		}
		actual := typed.Value
		if !map[string]bool{"string": true, "number": true, "bool": true, "array": true, "object": true, "null": true}[actual] {
			actual = "invalid_value"
		}
		return fmt.Sprintf("json_type_mismatch field=%s expected=%s actual=%s offset=%d", field, typed.Type, actual, typed.Offset)
	}
	return fmt.Sprintf("json_decode_failed error_type=%T", err)
}
func decodeRecordingWrite(w http.ResponseWriter, r *http.Request, out any, reason *string) bool {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		*reason = fmt.Sprintf("request_body_read_failed error_type=%T", err)
		writeJSON(w, 400, errBody("INVALID_PARAMS", "读取请求体失败"))
		return false
	}
	if len(data) == 0 {
		return true
	}
	if err = json.Unmarshal(data, out); err != nil {
		*reason = recordingDecodeReason(err)
		writeJSON(w, 400, errBody("INVALID_PARAMS", "请求体不是合法 JSON"))
		return false
	}
	return true
}
func (s *server) logRecordingWriteRejection(r *http.Request, code, stage, reason, id, source string) {
	if source != "" && source != "mobile_app" && source != "capture_app" {
		source = "[redacted]"
	}
	s.logger.Warn("[recording-result] 写入拒绝: " + diagnostics.Message("recording.write.rejected", map[string]any{
		"relay":       safeRecordingLogID(r.Header.Get(relay.InternalRequestIDHeader)),
		"recordingId": safeRecordingLogID(id), "sourceType": safeRecordingLogID(source),
		"code": code, "stage": stage, "reason": reason,
	}))
}

func recordingValidationReason(err error) string {
	switch err.Error() {
	case "recordingId is required", "recordingId is invalid", "transcript or summary is required",
		"ossTaskId requires a nonempty task ID (max 256 bytes) and ossUrl":
		return err.Error()
	}
	if strings.Contains(err.Error(), "recording_type") {
		return "recording_type_must_be_normal_or_call"
	}
	return "result_write_validation_failed"
}

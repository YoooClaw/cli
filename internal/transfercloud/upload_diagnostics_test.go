package transfercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/YoooClaw/cli/internal/output"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

func TestUploadDiagnosticPreservesContractAndExcludesRawSDKData(t *testing.T) {
	raw := &oss.ServiceError{Code: "RequestTimeTooSkewed", StatusCode: 403, RequestID: "request-123", EC: "0002-00000504",
		Message: "Request time differs. SecurityToken=secret-message", RequestTarget: "https://host/key?Signature=secret-signature",
		Snapshot: []byte("secret-snapshot"), Headers: http.Header{"Authorization": []string{"secret-auth"}}}
	detail := uploadFailureDetail("oss_upload", 2, time.Now(), raw)
	for _, want := range []string{"attempt=2", "http_status=403", "oss_code=RequestTimeTooSkewed", "oss_request_id=request-123", "oss_ec=0002-00000504"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("missing %q: %s", want, detail)
		}
	}
	if strings.Contains(detail, "secret") {
		t.Fatal(detail)
	}
	public := failure("CLOUD_UPLOAD_FAILED", "original message")
	wrapped := withUploadDiagnostics(public, []string{detail})
	var coded *errs.Error
	if wrapped.Error() != public.Error() || !errors.As(wrapped, &coded) || coded.Code != "YOOOCLAW_TRANSFER_CLOUD_UPLOAD_FAILED" {
		t.Fatalf("changed public error: %v", wrapped)
	}
	var service *oss.ServiceError
	if errors.As(wrapped, &service) {
		t.Fatal("SDK error exposed through public chain")
	}
}

func TestRecordingUploadFailureRetainsOSSAndConfirmationDiagnostics(t *testing.T) {
	var completes int
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/create":
			reply(w, recordingTask())
		case apiPath + "/complete":
			completes++
			w.WriteHeader(409)
			fmt.Fprint(w, `{"message":"Object not committed","securityToken":"secret-complete-body"}`)
		default:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(403)
			fmt.Fprint(w, `<Error><Code>RequestTimeTooSkewed</Code><Message>Request time differs. SecurityToken=secret-oss-body</Message><RequestId>request-123</RequestId><EC>0002-00000504</EC></Error>`)
		}
	})
	file := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(file, []byte("recording"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := c.UploadRecordingWithTask(context.Background(), file, nil)
	var diagnostic interface{ Diagnostic() string }
	if err == nil || !errors.As(err, &diagnostic) {
		t.Fatalf("missing diagnostic: %v", err)
	}
	detail := diagnostic.Diagnostic()
	for _, want := range []string{"stage=oss_upload", "operation=PutObject", "oss_code=RequestTimeTooSkewed", "oss_request_id=request-123", "stage=complete_after_upload_error", "http_status=409", "attempt=4", "Request time differs.", "Object not committed"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("missing %q: %s", want, detail)
		}
	}
	if completes != 4 || strings.Contains(detail, "secret") || strings.Contains(err.Error(), "RequestTimeTooSkewed") {
		t.Fatalf("complete=%d public=%v diagnostic=%s", completes, err, detail)
	}
}

func TestRecordingUploadCompleteFailureRetainsDiagnosticWithoutReupload(t *testing.T) {
	var puts, completes int
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/create":
			reply(w, recordingTask())
		case apiPath + "/complete":
			completes++
			w.WriteHeader(503)
			fmt.Fprint(w, `{"message":"Storage unavailable","apiKey":"secret-response"}`)
		default:
			puts++
			b, _ := io.ReadAll(r.Body)
			crc := oss.NewCRC64(0)
			crc.Write(b)
			w.Header().Set("x-oss-hash-crc64ecma", fmt.Sprint(crc.Sum64()))
			w.Header().Set("ETag", `"test"`)
			w.WriteHeader(200)
		}
	})
	file := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(file, []byte("recording"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := c.UploadRecordingWithTask(context.Background(), file, nil)
	var diagnostic interface{ Diagnostic() string }
	var coded *errs.Error
	if !errors.As(err, &diagnostic) || !errors.As(err, &coded) || coded.Code != "YOOOCLAW_TRANSFER_CLOUD_COMPLETE_FAILED" {
		t.Fatalf("error=%v", err)
	}
	detail := diagnostic.Diagnostic()
	if puts != 1 || completes != 4 || !strings.Contains(detail, "stage=complete attempt=4") || !strings.Contains(detail, "http_status=503") || strings.Contains(detail, "secret") {
		t.Fatalf("puts=%d completes=%d detail=%s", puts, completes, detail)
	}
}

func TestUploadDiagnosticPublicRendering(t *testing.T) {
	for _, code := range []string{"CLOUD_UPLOAD_FAILED", "CLOUD_COMPLETE_FAILED"} {
		public := failure(code, "public message")
		wrapped := withUploadDiagnostics(public, []string{"LOCAL_ONLY_DIAGNOSTIC"})
		for _, format := range []output.Format{output.JSON, output.Pretty} {
			var buf bytes.Buffer
			output.RenderError(&buf, wrapped, format)
			var value struct {
				Error struct {
					Code    string
					Message string
				}
			}
			if err := json.Unmarshal(buf.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if value.Error.Code != "YOOOCLAW_TRANSFER_"+code || value.Error.Message != "public message" || strings.Contains(buf.String(), "LOCAL_ONLY") {
				t.Fatal(buf.String())
			}
		}
	}
}
func TestUploadDiagnosticFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{failure("CLOUD_REFRESH_FAILED", "refresh failed"), "service_error"},
		{&oss.ClientError{Code: "SerializationError", Message: "bad response"}, "sdk_error"},
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"},
	} {
		if got := uploadFailureDetail("oss_upload", 1, time.Now(), tc.err); !strings.Contains(got, "reason="+tc.reason) {
			t.Fatal(got)
		}
	}
}

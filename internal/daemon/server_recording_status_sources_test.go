package daemon

import (
	"fmt"
	"github.com/YoooClaw/cli/internal/recording"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordingStatusSourceLookup(t *testing.T) {
	for _, tc := range []struct {
		name, mobileLabel, captureLabel, source, broken, wantSource, wantCode string
	}{
		{name: "legacy mobile", mobileLabel: "phone-a", wantSource: "mobile_app"},
		{name: "capture without source", captureLabel: "phone-a", wantSource: "capture_app"},
		{name: "explicit capture", captureLabel: "phone-a", source: "capture_app", wantSource: "capture_app"},
		{name: "explicit source does not fall back", captureLabel: "phone-a", source: "mobile_app", wantCode: "NOT_FOUND"},
		{name: "unknown source", source: "unknown", wantCode: "INVALID_PARAMS"},
		{name: "missing", wantCode: "NOT_FOUND"},
		{name: "hidden capture", captureLabel: "phone-b", wantCode: "NOT_FOUND"},
		{name: "hidden mobile", mobileLabel: "phone-b", wantCode: "NOT_FOUND"},
		{name: "duplicate visible", mobileLabel: "phone-a", captureLabel: "phone-a", wantCode: "SOURCE_CONFLICT"},
		{name: "explicit resolves duplicate", mobileLabel: "phone-a", captureLabel: "phone-a", source: "capture_app", wantSource: "capture_app"},
		{name: "hidden duplicate does not leak", mobileLabel: "phone-b", captureLabel: "phone-a", wantSource: "capture_app"},
		{name: "capture unavailable missing", broken: "capture_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "mobile unavailable missing", broken: "mobile_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "healthy match with broken capture", mobileLabel: "phone-a", broken: "capture_app", wantSource: "mobile_app"},
		{name: "healthy match with broken mobile", captureLabel: "phone-a", broken: "mobile_app", wantSource: "capture_app"},
		{name: "explicit broken source", mobileLabel: "phone-a", broken: "capture_app", source: "capture_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts := newTestServer(t, "")
			mobile := withRecordings(t, srv)
			captureRoot := withCaptureRecordings(t, srv)
			for _, pair := range []struct {
				store *recording.Storage
				label string
			}{{mobile, tc.mobileLabel}, {srv.captureRecordingStorage, tc.captureLabel}} {
				if pair.label != "" {
					if _, err := pair.store.Ingest("same-id", recording.Metadata{Name: "test"}, pair.label); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.broken != "" {
				root := captureRoot
				if tc.broken == "mobile_app" {
					root = filepath.Dir(mobile.AudioDir())
				}
				if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				var broken *recording.Storage
				if tc.broken == "mobile_app" {
					broken = recording.NewStorage(root, srv.logger)
					srv.recordingStorage = broken
				} else {
					broken = recording.NewCaptureStorage(root, srv.logger)
					srv.captureRecordingStorage = broken
				}
				if err := broken.Init(); err != nil {
					t.Fatal(err)
				}
				if broken.LoadError() == nil {
					t.Fatal("expected degraded storage")
				}
			}
			body := `{"recordingId":"same-id"}`
			if tc.source != "" {
				body = fmt.Sprintf(`{"recordingId":"same-id","sourceType":%q}`, tc.source)
			}
			resp := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.status", "phone-a", body)
			if tc.wantCode != "" {
				if got := errorCode(resp); got != tc.wantCode {
					t.Fatalf("want %s, response=%+v", tc.wantCode, resp)
				}
				return
			}
			data := gatewayData(t, resp)
			if data["recordingId"] != "same-id" || data["sourceType"] != tc.wantSource {
				t.Fatalf("response=%+v", resp)
			}
		})
	}
}

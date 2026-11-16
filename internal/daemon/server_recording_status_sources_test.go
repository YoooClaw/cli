package daemon

import (
	"fmt"
	"github.com/YoooClaw/cli/internal/recording"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordingStatusSourceLookup(t *testing.T) {
	for _, tc := range []struct {
		name, mobileLabel, captureLabel, source, broken, missing, wantSource, wantCode string
	}{
		{name: "blank source defaults to mobile", mobileLabel: "phone-a", source: "  ", wantSource: "mobile_app"},
		{name: "explicit empty source defaults to mobile", mobileLabel: "phone-a", source: "<empty>", wantSource: "mobile_app"},
		{name: "both degraded", broken: "mobile_app,capture_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "explicit degraded mobile", broken: "mobile_app", source: "mobile_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "missing capture does not affect default", mobileLabel: "phone-a", missing: "capture_app", wantSource: "mobile_app"},
		{name: "both missing", missing: "both", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "healthy hit missing capture", mobileLabel: "phone-a", missing: "capture_app", wantSource: "mobile_app"},
		{name: "explicit missing capture", source: "capture_app", missing: "capture_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},

		{name: "legacy mobile", mobileLabel: "phone-a", wantSource: "mobile_app"},
		{name: "capture without source defaults to mobile", captureLabel: "phone-a", wantCode: "NOT_FOUND"},
		{name: "explicit capture", captureLabel: "phone-a", source: "capture_app", wantSource: "capture_app"},
		{name: "explicit source does not fall back", captureLabel: "phone-a", source: "mobile_app", wantCode: "NOT_FOUND"},
		{name: "unknown source", source: "unknown", wantCode: "INVALID_PARAMS"},
		{name: "missing", wantCode: "NOT_FOUND"},
		{name: "hidden capture", captureLabel: "phone-b", wantCode: "NOT_FOUND"},
		{name: "hidden mobile", mobileLabel: "phone-b", wantCode: "NOT_FOUND"},
		{name: "duplicate visible defaults to mobile", mobileLabel: "phone-a", captureLabel: "phone-a", wantSource: "mobile_app"},
		{name: "explicit resolves duplicate", mobileLabel: "phone-a", captureLabel: "phone-a", source: "capture_app", wantSource: "capture_app"},
		{name: "hidden duplicate does not leak", mobileLabel: "phone-b", captureLabel: "phone-a", wantCode: "NOT_FOUND"},
		{name: "capture unavailable does not affect default", mobileLabel: "phone-a", broken: "capture_app", wantSource: "mobile_app"},
		{name: "mobile unavailable missing", broken: "mobile_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "healthy match with broken capture", mobileLabel: "phone-a", broken: "capture_app", wantSource: "mobile_app"},
		{name: "broken mobile is unavailable by default", captureLabel: "phone-a", broken: "mobile_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "explicit broken source", mobileLabel: "phone-a", broken: "capture_app", source: "capture_app", wantCode: "RECORDING_LIST_UNAVAILABLE"},

		{name: "all mobile hit", mobileLabel: "phone-a", source: "all", wantSource: "mobile_app"},
		{name: "all capture hit", captureLabel: "phone-a", source: "all", wantSource: "capture_app"},
		{name: "all duplicate visible conflicts", mobileLabel: "phone-a", captureLabel: "phone-a", source: "all", wantCode: "SOURCE_CONFLICT"},
		{name: "all hidden duplicate does not leak", mobileLabel: "phone-b", captureLabel: "phone-a", source: "all", wantSource: "capture_app"},
		{name: "all missing capture still resolves mobile", mobileLabel: "phone-a", missing: "capture_app", source: "all", wantSource: "mobile_app"},
		{name: "all missing capture without hit is unavailable", missing: "capture_app", source: "all", wantCode: "RECORDING_LIST_UNAVAILABLE"},
		{name: "all healthy mobile with broken capture", mobileLabel: "phone-a", broken: "capture_app", source: "all", wantSource: "mobile_app"},
		{name: "all healthy capture with broken mobile", captureLabel: "phone-a", broken: "mobile_app", source: "all", wantSource: "capture_app"},
		{name: "all both degraded", broken: "mobile_app,capture_app", source: "all", wantCode: "RECORDING_LIST_UNAVAILABLE"},
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
			for _, brokenSource := range strings.Split(tc.broken, ",") {
				if brokenSource == "" {
					continue
				}
				root := captureRoot
				if brokenSource == "mobile_app" {
					root = filepath.Dir(mobile.AudioDir())
				}
				if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				var broken *recording.Storage
				if brokenSource == "mobile_app" {
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
			if tc.missing == "capture_app" || tc.missing == "both" {
				srv.captureRecordingStorage = nil
			}
			if tc.missing == "both" {
				srv.recordingStorage = nil
			}
			body := `{"recordingId":"same-id"}`
			if tc.source != "" {
				source := tc.source
				if source == "<empty>" {
					source = ""
				}
				body = fmt.Sprintf(`{"recordingId":"same-id","sourceType":%q}`, source)
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

func TestRecordingListDefaultsToMobileSource(t *testing.T) {
	srv, ts := newTestServer(t, "")
	withRecordings(t, srv)
	srv.captureRecordingStorage = nil
	// 省略 sourceType 时只读 mobile_app，Capture 缺失不应影响默认列表。
	resp := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.list", "phone-a", `{}`)
	gatewayData(t, resp)
	// 显式指定不可用的 Capture 才返回来源不可用。
	resp = asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.list", "phone-a", `{"sourceType":"capture_app"}`)
	if errorCode(resp) != "RECORDING_LIST_UNAVAILABLE" {
		t.Fatal(resp)
	}
}

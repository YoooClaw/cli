package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/recording"
)

type daemonFakeExportCloud struct {
	uploads int
	links   int
}

func (c *daemonFakeExportCloud) UploadRecording(ctx context.Context, path string, onTask func(string, string) error) (recording.ExportUpload, error) {
	c.uploads++
	info, err := os.Stat(path)
	if err != nil {
		return recording.ExportUpload{}, err
	}
	if onTask != nil {
		if err := onTask("export-task-1", "users/audio/20261110/0123456789abcdef0123456789abcdef.m4a"); err != nil {
			return recording.ExportUpload{}, err
		}
	}
	return recording.ExportUpload{
		TaskID: "export-task-1", ObjectKey: "users/audio/20261110/0123456789abcdef0123456789abcdef.m4a",
		SizeBytes: info.Size(), SignedURL: "https://bucket.oss-cn-hangzhou.aliyuncs.com/a.m4a?signature=s",
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (c *daemonFakeExportCloud) RecordingDownloadLink(ctx context.Context, taskID string) (recording.DownloadLink, error) {
	c.links++
	return recording.DownloadLink{URL: "https://bucket.oss-cn-hangzhou.aliyuncs.com/a.m4a?signature=r", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func seedExportableRecording(t *testing.T, storage *recording.Storage, id, label string) {
	t.Helper()
	if _, err := storage.Ingest(id, recording.Metadata{Name: id, CreatedAt: "2026-11-09T10:00:00+08:00", DurationSec: 90}, label); err != nil {
		t.Fatal(err)
	}
	path := storage.AudioFilePath(id, "https://example.test/audio.m4a")
	if err := os.WriteFile(path, []byte("audio-bytes-"+id), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile(id, filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
}

func waitGatewayExport(t *testing.T, url, label, recordingID, exportID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := asClient(t, "POST", url+"/gateway/recordings.export.status", label,
			`{"recordingId":"`+recordingID+`","exportId":"`+exportID+`"}`)
		data := gatewayData(t, resp)
		state, _ := data["state"].(string)
		if state == "ready" || state == "failed" || state == "expired" {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("export did not finish: %+v", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRecordingExportRoutesStartAndStatus(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	seedExportableRecording(t, storage, "rec_export", "phone-a")
	seedExportableRecording(t, storage, "rec_other", "phone-a")
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(label string) (recording.ExportCloud, error) {
		if label != "phone-a" {
			t.Fatalf("unexpected export label %q", label)
		}
		return cloud, nil
	}

	start := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-1"}`))
	exportID := asString(start["exportId"])
	if exportID == "" {
		t.Fatalf("start=%+v", start)
	}
	status := waitGatewayExport(t, ts.URL, "phone-a", "rec_export", exportID)
	if status["state"] != "ready" {
		t.Fatalf("status=%+v", status)
	}
	result, _ := status["result"].(map[string]any)
	payload, _ := result["writePayload"].(map[string]any)
	if payload["recordingId"] != "rec_export" || payload["ossTaskId"] != "export-task-1" || payload["ossUrl"] == "" {
		t.Fatalf("payload=%+v", payload)
	}
	meta, _ := payload["recording"].(map[string]any)
	if meta["recording_type"] != recording.RecordingTypeNormal {
		t.Fatalf("recording=%+v", meta)
	}
	transfer, _ := result["transfer"].(map[string]any)
	if transfer["fileName"] != "rec_export.m4a" || transfer["sizeBytes"] != float64(len("audio-bytes-rec_export")) {
		t.Fatalf("transfer=%+v", transfer)
	}

	// 同一 requestId 的终态重放返回同一个 exportId，且不重复上传。
	again := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-1"}`))
	if asString(again["exportId"]) != exportID || cloud.uploads != 1 {
		t.Fatalf("idempotent replay=%+v uploads=%d", again, cloud.uploads)
	}

	// requestId 已属于另一条录音。
	conflict := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_other","requestId":"req-1"}`)
	if code := errorCode(conflict); code != "REQUEST_CONFLICT" {
		t.Fatalf("conflict=%+v", conflict)
	}

	// 其他客户端既看不到录音，也读不到导出。
	for _, remote := range []struct {
		method string
		body   string
	}{
		{"recordings.export.start", `{"recordingId":"rec_export","requestId":"req-x"}`},
		{"recordings.export.status", `{"recordingId":"rec_export","exportId":"` + exportID + `"}`},
	} {
		if code := errorCode(asClient(t, "POST", ts.URL+"/gateway/"+remote.method, "phone-b", remote.body)); code != "NOT_FOUND" {
			t.Fatalf("%s exposed to another client: %+v", remote.method, remote.body)
		}
	}
}

func TestRecordingExportStartReplaysAfterSourceBecomesUnreadable(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	seedExportableRecording(t, storage, "rec_export", "phone-a")
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(string) (recording.ExportCloud, error) { return cloud, nil }

	start := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-replay"}`))
	exportID := asString(start["exportId"])
	waitGatewayExport(t, ts.URL, "phone-a", "rec_export", exportID)
	if err := os.Remove(storage.AudioFilePath("rec_export", "https://example.test/audio.m4a")); err != nil {
		t.Fatal(err)
	}

	replay := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-replay"}`))
	if asString(replay["exportId"]) != exportID || asString(replay["state"]) != "ready" {
		t.Fatalf("replay=%+v", replay)
	}
	if cloud.uploads != 1 {
		t.Fatalf("replay triggered another upload: %d", cloud.uploads)
	}

	// 同一 requestId 属于另一条不可导出录音时，冲突判定同样优先于来源检查。
	if _, err := storage.Ingest("rec_plain", recording.Metadata{Name: "无音频"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	conflict := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_plain","requestId":"req-replay"}`)
	if code := errorCode(conflict); code != "REQUEST_CONFLICT" {
		t.Fatalf("conflict=%+v", conflict)
	}
}

func TestRecordingExportRoutesValidation(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	if _, err := storage.Ingest("rec_plain", recording.Metadata{Name: "无音频"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	seedExportableRecording(t, storage, "rec_export", "phone-a")
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(string) (recording.ExportCloud, error) { return cloud, nil }

	for name, tc := range map[string]struct {
		method string
		body   string
		code   string
	}{
		"missing requestId":  {"recordings.export.start", `{"recordingId":"rec_export"}`, "INVALID_PARAMS"},
		"unsafe recordingId": {"recordings.export.start", `{"recordingId":"../x","requestId":"r"}`, "INVALID_PARAMS"},
		"unknown recording":  {"recordings.export.start", `{"recordingId":"nope","requestId":"r"}`, "NOT_FOUND"},
		"not exportable":     {"recordings.export.start", `{"recordingId":"rec_plain","requestId":"r"}`, "SOURCE_UNREADABLE"},
		"missing exportId":   {"recordings.export.status", `{"recordingId":"rec_export"}`, "INVALID_PARAMS"},
		"unknown export":     {"recordings.export.status", `{"recordingId":"rec_export","exportId":"export-nope"}`, "EXPORT_NOT_FOUND"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := asClient(t, "POST", ts.URL+"/gateway/"+tc.method, "phone-a", tc.body)
			if code := errorCode(resp); code != tc.code {
				t.Fatalf("resp=%+v", resp)
			}
		})
	}

	srv.exportCloudFn = func(string) (recording.ExportCloud, error) {
		return nil, os.ErrPermission
	}
	resp := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-cred"}`)
	if code := errorCode(resp); code != "EXPORT_UPLOAD_FAILED" {
		t.Fatalf("missing credentials resp=%+v", resp)
	}
	if cloud.uploads != 0 {
		t.Fatalf("uploads=%d", cloud.uploads)
	}
}

func TestGatewayHealthListsRecordingExportMethods(t *testing.T) {
	_, ts := newTestServer(t, "")
	data := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/health", "", `{}`))
	features, _ := data["features"].(map[string]any)
	methods, _ := features["methods"].([]any)
	found := map[string]bool{}
	for _, method := range methods {
		found[asString(method)] = true
	}
	for _, method := range []string{"recordings.export.start", "recordings.export.status"} {
		if !found[method] {
			t.Fatalf("health methods missing %s: %v", method, methods)
		}
	}
}

func errorCode(resp map[string]any) string {
	errObj, _ := resp["error"].(map[string]any)
	return asString(errObj["code"])
}

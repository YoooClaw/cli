package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func waitGatewayExport(t *testing.T, url, label, recordingID, exportID string, source ...string) map[string]any {
	t.Helper()
	body := `{"recordingId":"` + recordingID + `","exportId":"` + exportID + `"`
	if len(source) > 0 && source[0] != "" {
		body += `,"sourceType":"` + source[0] + `"`
	}
	body += `}`
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := asClient(t, "POST", url+"/gateway/recordings.export.status", label, body)
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
	withCaptureRecordings(t, srv)
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
	if start["sourceType"] != recording.SourceMobileApp {
		t.Fatalf("start sourceType=%v, want %s", start["sourceType"], recording.SourceMobileApp)
	}
	// start 在导出完成前给出源音频大小的预估，供客户端规划等待预算。
	if start["sizeBytes"] != float64(len("audio-bytes-rec_export")) {
		t.Fatalf("start sizeBytes=%v", start["sizeBytes"])
	}
	status := waitGatewayExport(t, ts.URL, "phone-a", "rec_export", exportID)
	if status["state"] != "ready" {
		t.Fatalf("status=%+v", status)
	}
	if status["sizeBytes"] != float64(len("audio-bytes-rec_export")) {
		t.Fatalf("status sizeBytes=%v", status["sizeBytes"])
	}
	if status["sourceType"] != recording.SourceMobileApp {
		t.Fatalf("status sourceType=%v, want %s", status["sourceType"], recording.SourceMobileApp)
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
	if again["sizeBytes"] != float64(len("audio-bytes-rec_export")) {
		t.Fatalf("replay sizeBytes=%v", again["sizeBytes"])
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

func TestCaptureRecordingExportRoutesStartAndStatus(t *testing.T) {
	srv, ts := newTestServer(t, "")
	withRecordings(t, srv)
	capture := withCaptureRecordings(t, srv)
	seedExportableRecording(t, srv.captureRecordingStorage, "cap_export", "phone-a")
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(label string) (recording.ExportCloud, error) {
		if label != "phone-a" {
			t.Fatalf("unexpected export label %q", label)
		}
		return cloud, nil
	}

	start := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"cap_export","requestId":"cap-req-1","sourceType":"capture_app"}`))
	exportID := asString(start["exportId"])
	if exportID == "" {
		t.Fatalf("start=%+v", start)
	}
	if start["sourceType"] != recording.SourceCaptureApp {
		t.Fatalf("start sourceType=%v, want %s", start["sourceType"], recording.SourceCaptureApp)
	}
	status := waitGatewayExport(t, ts.URL, "phone-a", "cap_export", exportID, recording.SourceCaptureApp)
	if status["state"] != "ready" {
		t.Fatalf("status=%+v", status)
	}
	if status["sourceType"] != recording.SourceCaptureApp {
		t.Fatalf("status sourceType=%v, want %s", status["sourceType"], recording.SourceCaptureApp)
	}
	result, _ := status["result"].(map[string]any)
	payload, _ := result["writePayload"].(map[string]any)
	// 接收方必须能把导出文件写回 Capture 来源。
	if payload["recordingId"] != "cap_export" || payload["sourceType"] != recording.SourceCaptureApp || payload["ossUrl"] == "" {
		t.Fatalf("payload=%+v", payload)
	}
	transfer, _ := result["transfer"].(map[string]any)
	if transfer["fileName"] != "cap_export.m4a" || transfer["sizeBytes"] != float64(len("audio-bytes-cap_export")) {
		t.Fatalf("transfer=%+v", transfer)
	}
	// 导出记录保存在 Capture 索引中，不进移动端索引。
	if records := recording.ReadIndex(capture); len(records) != 1 || len(records[0].Exports) != 1 {
		t.Fatalf("capture index exports=%+v", records)
	}
	if entries := srv.recordingStorage.ListAll(); len(entries) != 0 {
		t.Fatalf("capture export leaked to mobile index: %+v", entries)
	}

	// 同一 requestId 重放返回同一个 exportId，且不重复上传。
	again := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"cap_export","requestId":"cap-req-1","sourceType":"capture_app"}`))
	if asString(again["exportId"]) != exportID || cloud.uploads != 1 {
		t.Fatalf("idempotent replay=%+v uploads=%d", again, cloud.uploads)
	}

	// 省略 sourceType 默认 mobile_app，不能看到仅存在于 Capture 的录音。
	for _, body := range []string{
		`{"recordingId":"cap_export","requestId":"cap-req-1"}`,
		`{"recordingId":"cap_export","exportId":"` + exportID + `"}`,
	} {
		method := "recordings.export.start"
		if strings.Contains(body, "exportId") {
			method = "recordings.export.status"
		}
		resp := asClient(t, "POST", ts.URL+"/gateway/"+method, "phone-a", body)
		if code := errorCode(resp); code != "NOT_FOUND" {
			t.Fatalf("default source must not see Capture: %s code=%s", method, code)
		}
	}
	// 显式 all 可以在两个来源中找到同一个 Capture 导出。
	for _, body := range []string{
		`{"recordingId":"cap_export","requestId":"cap-req-1","sourceType":"all"}`,
		`{"recordingId":"cap_export","exportId":"` + exportID + `","sourceType":"all"}`,
	} {
		method := "recordings.export.start"
		if strings.Contains(body, "exportId") {
			method = "recordings.export.status"
		}
		data := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/"+method, "phone-a", body))
		if data["sourceType"] != recording.SourceCaptureApp || data["exportId"] != exportID {
			t.Fatal(data)
		}
	}
	if code := errorCode(asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"cap_export","requestId":"cap-req-3","sourceType":"unknown"}`)); code != "INVALID_PARAMS" {
		t.Fatalf("invalid sourceType code=%s", code)
	}
}

func TestRecordingExportStartReplaysAfterSourceBecomesUnreadable(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	withCaptureRecordings(t, srv)
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
	withCaptureRecordings(t, srv)
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

func TestRecordingExportAllSourcesAndReplay(t *testing.T) {
	srv, ts := newTestServer(t, "")
	mobile := withRecordings(t, srv)
	withCaptureRecordings(t, srv)
	t.Cleanup(srv.closeRecordingExports)
	capture := srv.captureRecordingStorage
	seedExportableRecording(t, capture, "same", "phone-a")
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(string) (recording.ExportCloud, error) { return cloud, nil }

	start := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"request-all","sourceType":"all"}`))
	id := asString(start["exportId"])
	if start["sourceType"] != "capture_app" {
		t.Fatal(start)
	}
	waitGatewayExport(t, ts.URL, "phone-a", "same", id, "all")

	// 保留任务优先：另一来源后来出现同 UUID 也不重定向。
	seedExportableRecording(t, mobile, "same", "phone-a")
	for _, body := range []string{
		`{"recordingId":"same","requestId":"request-all","sourceType":"all"}`,
		`{"recordingId":"same","exportId":"` + id + `","sourceType":"all"}`,
	} {
		method := "start"
		if strings.Contains(body, "exportId") {
			method = "status"
		}
		result := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export."+method, "phone-a", body))
		if result["exportId"] != id || result["sourceType"] != "capture_app" {
			t.Fatal(result)
		}
	}

	// all 命中两个可见来源时要求显式指定来源。
	duplicate := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"new-request","sourceType":"all"}`)
	if errorCode(duplicate) != "SOURCE_CONFLICT" {
		t.Fatal(duplicate)
	}
	// 请求键已属于另一录音时返回 REQUEST_CONFLICT。
	seedExportableRecording(t, mobile, "other", "phone-a")
	conflict := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"other","requestId":"request-all","sourceType":"all"}`)
	if errorCode(conflict) != "REQUEST_CONFLICT" {
		t.Fatal(conflict)
	}
	if cloud.uploads != 1 {
		t.Fatalf("uploads=%d", cloud.uploads)
	}

	// 来源不可用不会掩盖保留任务，但无法确认新请求键时返回存储不可用。
	srv.recordingStorage = nil
	result := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"request-all","sourceType":"all"}`))
	if result["exportId"] != id {
		t.Fatal(result)
	}
	missing := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"another","sourceType":"all"}`)
	if errorCode(missing) != "RECORDING_LIST_UNAVAILABLE" {
		t.Fatal(missing)
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

func TestRecordingExportExplicitSourceAndReplay(t *testing.T) {
	srv, ts := newTestServer(t, "")
	mobile := withRecordings(t, srv)
	withCaptureRecordings(t, srv)
	t.Cleanup(srv.closeRecordingExports)
	capture := srv.captureRecordingStorage
	seedExportableRecording(t, capture, "same", "phone-a")
	cloud := &daemonFakeExportCloud{}
	srv.exportCloudFn = func(string) (recording.ExportCloud, error) { return cloud, nil }

	// 省略 sourceType 默认 mobile_app，不会看到仅存在于 Capture 的录音。
	defaultStart := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"request-capture"}`)
	if errorCode(defaultStart) != "NOT_FOUND" {
		t.Fatal(defaultStart)
	}

	start := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"request-capture","sourceType":"capture_app"}`))
	id := asString(start["exportId"])
	if start["sourceType"] != "capture_app" {
		t.Fatal(start)
	}
	waitGatewayExport(t, ts.URL, "phone-a", "same", id, "capture_app")

	// 保留任务优先于 UUID 查找：另一来源出现同 ID 记录也不影响重放。
	seedExportableRecording(t, mobile, "same", "phone-a")
	for _, body := range []string{
		`{"recordingId":"same","requestId":"request-capture","sourceType":"capture_app"}`,
		`{"recordingId":"same","exportId":"` + id + `","sourceType":"capture_app"}`,
	} {
		method := "start"
		if strings.Contains(body, "exportId") {
			method = "status"
		}
		result := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export."+method, "phone-a", body))
		if result["exportId"] != id || result["sourceType"] != "capture_app" {
			t.Fatal(result)
		}
	}

	// 默认来源是 mobile_app：同 ID 的 mobile 记录独立导出，不再与 Capture 冲突。
	mobileStart := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"request-mobile"}`))
	mobileID := asString(mobileStart["exportId"])
	if mobileStart["sourceType"] != "mobile_app" || mobileID == id {
		t.Fatal(mobileStart)
	}
	waitGatewayExport(t, ts.URL, "phone-a", "same", mobileID)

	// requestId 在同一来源内不能复用于其他录音。
	seedExportableRecording(t, mobile, "other", "phone-a")
	conflict := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"other","requestId":"request-mobile"}`)
	if errorCode(conflict) != "REQUEST_CONFLICT" {
		t.Fatal(conflict)
	}
	if cloud.uploads != 2 {
		t.Fatalf("uploads=%d", cloud.uploads)
	}

	// 默认来源不可用时返回存储不可用，而不是误报 NOT_FOUND。
	srv.recordingStorage = nil
	missing := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"another"}`)
	if errorCode(missing) != "RECORDING_LIST_UNAVAILABLE" {
		t.Fatal(missing)
	}
	// 显式 Capture 仍能重放保留任务。
	result := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"same","requestId":"request-capture","sourceType":"capture_app"}`))
	if result["exportId"] != id {
		t.Fatal(result)
	}
}

type blockingDaemonExportCloud struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingDaemonExportCloud) UploadRecording(ctx context.Context, path string, onTask func(string, string) error) (recording.ExportUpload, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return recording.ExportUpload{}, ctx.Err()
	}
	info, err := os.Stat(path)
	if err != nil {
		return recording.ExportUpload{}, err
	}
	if onTask != nil {
		if err := onTask("export-task-1", "users/audio/20261110/busy.m4a"); err != nil {
			return recording.ExportUpload{}, err
		}
	}
	return recording.ExportUpload{
		TaskID: "export-task-1", ObjectKey: "users/audio/20261110/busy.m4a",
		SizeBytes: info.Size(), SignedURL: "https://bucket.example/a.m4a?signature=s",
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (c *blockingDaemonExportCloud) RecordingDownloadLink(ctx context.Context, taskID string) (recording.DownloadLink, error) {
	return recording.DownloadLink{URL: "https://bucket.example/a.m4a?signature=r", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// 忙时要把同 scope 正在进行的导出摘要交还给客户端，让 App 能直接轮询现有
// exportId；跨 scope 的任务不可见，不能借此泄露。
func TestRecordingExportBusyReturnsInflightExport(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	withCaptureRecordings(t, srv)
	// 空 label 对所有客户端可见，phone-b 才能走到"录音可见但导出忙"的分支。
	seedExportableRecording(t, storage, "rec_export", "")
	cloud := &blockingDaemonExportCloud{started: make(chan struct{}), release: make(chan struct{})}
	srv.exportCloudFn = func(string) (recording.ExportCloud, error) { return cloud, nil }

	first := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-1"}`))
	<-cloud.started

	busy := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-2"}`)
	if code := errorCode(busy); code != "EXPORT_BUSY" {
		t.Fatalf("busy code=%q resp=%+v", code, busy)
	}
	errObj, _ := busy["error"].(map[string]any)
	details, _ := errObj["details"].(map[string]any)
	if details["exportId"] != first["exportId"] {
		t.Fatalf("busy details=%+v, want exportId=%v", details, first["exportId"])
	}
	if details["state"] != recording.ExportStatePreparing {
		t.Fatalf("busy state=%v", details["state"])
	}
	if details["sourceType"] != recording.SourceMobileApp || details["retryable"] != true {
		t.Fatalf("busy details=%+v", details)
	}

	// 另一客户端作用域拿不到别人的 exportId。
	crossScope := asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-b",
		`{"recordingId":"rec_export","requestId":"req-3"}`)
	if code := errorCode(crossScope); code != "EXPORT_BUSY" {
		t.Fatalf("cross-scope code=%q resp=%+v", code, crossScope)
	}
	crossErr, _ := crossScope["error"].(map[string]any)
	if leaked, ok := crossErr["details"].(map[string]any); ok {
		t.Fatalf("cross-scope busy leaked details: %+v", leaked)
	}

	// 同一 requestId 的重放仍然幂等，不会变成 BUSY。
	replay := gatewayData(t, asClient(t, "POST", ts.URL+"/gateway/recordings.export.start", "phone-a",
		`{"recordingId":"rec_export","requestId":"req-1"}`))
	if replay["exportId"] != first["exportId"] {
		t.Fatalf("replay=%+v", replay)
	}

	close(cloud.release)
	status := waitGatewayExport(t, ts.URL, "phone-a", "rec_export", asString(first["exportId"]))
	if status["state"] != "ready" {
		t.Fatalf("status=%+v", status)
	}
}

package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
)

func recordingTypePtr(v string) *string { return &v }

func withRecordingsAt(t *testing.T, srv *server, dir string) *recording.Storage {
	t.Helper()
	storage := recording.NewStorage(dir, srv.logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	srv.recordingStorage = storage
	srv.recordingEventLog = recording.NewEventLog(dir)
	srv.egress = NoopEgress{}
	return storage
}

func findRecordingItem(t *testing.T, resp map[string]any, id string) map[string]any {
	t.Helper()
	raw, _ := gatewayData(t, resp)["recordings"].([]any)
	for _, item := range raw {
		m, _ := item.(map[string]any)
		if m["recordingId"] == id {
			return m
		}
	}
	t.Fatalf("recording %s not in list: %+v", id, resp)
	return nil
}

func TestRecordingListExposesTypeAndExportable(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	if _, err := storage.Ingest("rec_call", recording.Metadata{Name: "通话", RecordingType: recordingTypePtr(recording.RecordingTypeCall)}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Ingest("rec_old", recording.Metadata{Name: "历史录音"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	path := storage.AudioFilePath("rec_call", "https://example.test/audio.m4a")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_call", filepath.Base(path)); err != nil {
		t.Fatal(err)
	}

	resp := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.list", "phone-a", `{}`)
	call := findRecordingItem(t, resp, "rec_call")
	if call["recording_type"] != recording.RecordingTypeCall {
		t.Fatalf("recording_type=%v", call["recording_type"])
	}
	if call["exportable"] != true {
		t.Fatalf("downloaded synced audio should be exportable: %+v", call)
	}
	old := findRecordingItem(t, resp, "rec_old")
	if old["recording_type"] != recording.RecordingTypeNormal {
		t.Fatalf("historical recording_type=%v", old["recording_type"])
	}
	if old["exportable"] != false {
		t.Fatalf("audio-less recording is exportable: %+v", old)
	}

	detail := gatewayData(t, asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.status", "phone-a", `{"recordingId":"rec_call"}`))
	if detail["recording_type"] != recording.RecordingTypeCall {
		t.Fatalf("detail recording_type=%v", detail["recording_type"])
	}
}

func TestRecordingListFailsOnCorruptIndexInsteadOfEmptyList(t *testing.T) {
	srv, ts := newTestServer(t, "")
	dir := t.TempDir()
	storage := withRecordingsAt(t, srv, dir)
	if _, err := storage.Ingest("rec_a", recording.Metadata{Name: "A"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.list", "phone-a", `{}`)
	if resp["ok"] != false {
		t.Fatalf("corrupt index returned success: %+v", resp)
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj["code"] != "RECORDING_LIST_UNAVAILABLE" {
		t.Fatalf("error=%+v", errObj)
	}
}

func TestRecordingDegradedStorageRejectsDomainCalls(t *testing.T) {
	srv, ts := newTestServer(t, "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "audio"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audio", "orphan.m4a"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := withRecordingsAt(t, srv, dir)
	if storage.LoadError() == nil {
		t.Fatal("missing index with assets should degrade storage")
	}

	for method, want := range map[string]string{
		"recordings.list":          "RECORDING_LIST_UNAVAILABLE",
		"recordings.status":        "RECORDING_LIST_UNAVAILABLE",
		"recordings.rename":        "RECORDING_STORAGE_UNAVAILABLE",
		"recordings.export.start":  "RECORDING_LIST_UNAVAILABLE",
		"recordings.export.status": "RECORDING_LIST_UNAVAILABLE",
	} {
		resp := asClient(t, http.MethodPost, ts.URL+"/gateway/"+method, "phone-a", `{"recordingId":"rec_a","name":"x"}`)
		if resp["ok"] != false {
			t.Fatalf("%s succeeded on degraded storage: %+v", method, resp)
		}
		errObj, _ := resp["error"].(map[string]any)
		if errObj["code"] != want {
			t.Fatalf("%s error=%+v", method, errObj)
		}
	}

	// 录音域降级不能拖垮 daemon 其他能力。
	status, err := http.Get(ts.URL + "/daemon/status")
	if err != nil {
		t.Fatal(err)
	}
	defer status.Body.Close()
	if status.StatusCode != http.StatusOK {
		t.Fatalf("daemon status=%d", status.StatusCode)
	}
}

func TestRecordingStatusExposesClassifiedError(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	if _, err := storage.Ingest("rec_full", recording.Metadata{Name: "空间不足"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	const url = "https://example.test/audio.m4a"
	if err := storage.SetResultAudioPending("rec_full", url); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.SetResultAudioFailedWithCode("rec_full", url, "音频下载失败: 空间不足", "STORAGE_FULL"); err != nil {
		t.Fatal(err)
	}
	detail := gatewayData(t, asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.status", "phone-a", `{"recordingId":"rec_full"}`))
	if detail["errorCode"] != "STORAGE_FULL" || detail["audio_status"] != recording.AudioStatusFailed || detail["error"] == nil {
		t.Fatalf("detail=%+v", detail)
	}

	// 新尝试清除旧错误码，旧 error/audio_status 语义不变。
	if err := storage.SetResultAudioPending("rec_full", url); err != nil {
		t.Fatal(err)
	}
	detail = gatewayData(t, asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.status", "phone-a", `{"recordingId":"rec_full"}`))
	if detail["errorCode"] != nil {
		t.Fatalf("detail=%+v", detail)
	}
}

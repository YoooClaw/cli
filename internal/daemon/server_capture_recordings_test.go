package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/capturerecording"
	"github.com/YoooClaw/cli/internal/recording"
)

func withCaptureRecordings(t *testing.T, srv *server) string {
	t.Helper()
	root := capturerecording.RecordingsPath(srv.ctx.Paths.Voice)
	store := recording.NewCaptureStorage(root, srv.logger)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	srv.captureRecordingStorage = store
	srv.captureRecordingEventLog = recording.NewEventLog(root)
	return root
}

func TestCaptureRecordingGatewayPersistsBeforeNotifying(t *testing.T) {
	srv, ts := newTestServer(t, "")
	withRecordings(t, srv)
	root := withCaptureRecordings(t, srv)
	type eventFrame struct {
		ClientLabel string                `json:"clientLabel"`
		Event       string                `json:"event"`
		Payload     recording.StatusEvent `json:"payload"`
	}
	events := make(chan eventFrame, 4)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var frame eventFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		events <- frame
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()
	srv.egress = NewProxyEgress(callback.URL, "", srv.logger)

	release := make(chan struct{})
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte("capture-audio"))
	}))
	defer audio.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	call := func(method, label, body string) map[string]any {
		return asClient(t, http.MethodPost, ts.URL+"/gateway/recordings."+method, label, body)
	}
	const label = "wuying-a"
	mobile := gatewayData(t, call("result.write", label, `{"recordingId":"mobile","transcript":{"text":"legacy"}}`))
	if mobile["sourceType"] != recording.SourceMobileApp {
		t.Fatalf("legacy source = %v", mobile)
	}
	write := gatewayData(t, call("result.write", label, fmt.Sprintf(
		`{"recordingId":"meeting","sourceType":"capture_app","ossUrl":%q}`, audio.URL+"/meeting.m4a")))
	if write["sourceType"] != recording.SourceCaptureApp || write["audio_status"] != recording.AudioStatusPending {
		t.Fatalf("unexpected Capture write response: %v", write)
	}
	if records := recording.ReadIndex(root); len(records) != 1 || records[0].ID != "meeting" ||
		records[0].SourceType != recording.SourceCaptureApp || records[0].AudioFile != "" {
		t.Fatalf("pending Capture index: %+v", records)
	}
	if srv.updateIdle(time.Now().Add(time.Hour)) || srv.activeRecordingDownloads() == 0 {
		t.Fatal("Capture download must block auto update")
	}
	close(release)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var downloaded eventFrame
	for downloaded.Payload.AudioStatus != recording.AudioStatusDownloaded {
		select {
		case downloaded = <-events:
		case <-timer.C:
			t.Fatal("missing downloaded recording.status callback")
		}
	}
	if downloaded.Event != "recording.status" || downloaded.ClientLabel != label ||
		downloaded.Payload.SourceType != recording.SourceCaptureApp || downloaded.Payload.AudioFile == "" {
		t.Fatalf("incorrect capture callback: %+v", downloaded)
	}
	data, err := os.ReadFile(filepath.Join(root, downloaded.Payload.AudioFile))
	if err != nil || string(data) != "capture-audio" {
		t.Fatalf("download was acknowledged before audio was saved: %v", err)
	}
	if records := recording.ReadIndex(root); len(records) != 1 ||
		records[0].AudioStatus != recording.AudioStatusDownloaded || records[0].AudioFile != downloaded.Payload.AudioFile {
		t.Fatalf("download was acknowledged before index was saved: %+v", records)
	}

	const captureQuery = `{"sourceType":"capture_app"}`
	const captureStatus = `{"recordingId":"meeting","sourceType":"capture_app"}`
	status := gatewayData(t, call("status", label, captureStatus))
	if status["audio_status"] != recording.AudioStatusDownloaded || status["audioFile"] != downloaded.Payload.AudioFile ||
		status["oss_audio_url"] != audio.URL+"/meeting.m4a" {
		t.Fatalf("status cannot reconcile the downloaded file: %v", status)
	}
	for _, tc := range []struct {
		label, query string
		ids          []string
	}{
		{label, `{}`, []string{"mobile"}},
		{label, `{"sourceType":"mobile_app"}`, []string{"mobile"}},
		{label, captureQuery, []string{"meeting"}},
		{"wuying-b", captureQuery, nil},
	} {
		if ids := recordingIDs(t, call("list", tc.label, tc.query)); !slices.Equal(ids, tc.ids) {
			t.Errorf("list %s %s = %v, want %v", tc.label, tc.query, ids, tc.ids)
		}
	}
	for _, tc := range []struct{ method, label, body, code string }{
		{"status", label, `{"recordingId":"meeting"}`, "NOT_FOUND"},
		{"status", "wuying-b", captureStatus, "NOT_FOUND"},
		{"list", label, `{"sourceType":"unknown"}`, "INVALID_PARAMS"},
		{"result.write", label, `{"recordingId":"meeting","summary":{"markdown":"wrong store"}}`, "SOURCE_CONFLICT"},
		{"result.write", "wuying-b", `{"recordingId":"meeting","sourceType":"capture_app","summary":{"markdown":"wrong owner"}}`, "NOT_FOUND"},
	} {
		resp := call(tc.method, tc.label, tc.body)
		problem, _ := resp["error"].(map[string]any)
		if problem["code"] != tc.code {
			t.Errorf("%s %s = %v, want %s", tc.method, tc.body, resp, tc.code)
		}
	}

	gatewayData(t, call("result.write", label, `{"recordingId":"meeting","sourceType":"capture_app","transcript":{"text":"updated"},"summary":{"markdown":"summary"}}`))
	gatewayData(t, call("rename", label, `{"recordingId":"meeting","sourceType":"capture_app","name":"new name"}`))
	reopened := recording.NewCaptureStorage(root, srv.logger)
	if err := reopened.Init(); err != nil {
		t.Fatal(err)
	}
	entries := reopened.ListAll()
	if len(entries) != 1 || entries[0].Metadata.Name != "new name" || entries[0].Status != recording.StatusTranscribed ||
		entries[0].AudioStatus != recording.AudioStatusDownloaded || entries[0].SummaryFile == "" {
		t.Fatalf("update did not survive reopening the full index: %+v", entries)
	}
	if legacy := srv.recordingStorage.ListAll(); len(legacy) != 1 || legacy[0].ID != "mobile" {
		t.Fatalf("Capture write leaked to mobile index: %+v", legacy)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "写入受理: recordingId=meeting, sourceType=capture_app, audio_status=pending") ||
		!strings.Contains(string(logs), "[recording-status] meeting -> synced, sourceType=capture_app, audio_status=downloaded") {
		t.Fatalf("missing Capture acceptance or download log: %s %v", logs, err)
	}
}

func TestCaptureRecordingDeleteRequiresSource(t *testing.T) {
	srv, ts := newTestServer(t, "")
	withRecordings(t, srv)
	root := withCaptureRecordings(t, srv)
	if _, err := srv.recordingStorage.Ingest("mobile", recording.Metadata{Name: "手机录音"}, "wuying-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.captureRecordingStorage.Ingest("meeting", recording.Metadata{Name: "Capture 录音"}, "wuying-a"); err != nil {
		t.Fatal(err)
	}
	call := func(label, body string) map[string]any {
		return asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.delete", label, body)
	}
	codeOf := func(resp map[string]any) string {
		problem, _ := resp["error"].(map[string]any)
		code, _ := problem["code"].(string)
		return code
	}

	// Omitting sourceType keeps the historical mobile_app default.
	if resp := call("wuying-a", `{"recordingId":"meeting"}`); codeOf(resp) != "NOT_FOUND" {
		t.Fatalf("default delete must not reach capture: %v", resp)
	}
	if resp := call("wuying-a", `{"recordingId":"meeting","sourceType":"unknown"}`); codeOf(resp) != "INVALID_PARAMS" {
		t.Fatalf("invalid sourceType = %v", resp)
	}
	if resp := call("wuying-b", `{"recordingId":"meeting","sourceType":"capture_app","deleteRemote":true}`); codeOf(resp) != "NOT_FOUND" {
		t.Fatalf("delete must stay inside the client scope: %v", resp)
	}
	if records := recording.ReadIndex(root); len(records) != 1 {
		t.Fatalf("failed deletes changed the capture index: %+v", records)
	}

	data := gatewayData(t, call("wuying-a", `{"recordingId":"meeting","sourceType":"capture_app","deleteRemote":true}`))
	if data["sourceType"] != recording.SourceCaptureApp || data["deletedRemote"] != true {
		t.Fatalf("unexpected delete response: %v", data)
	}
	if records := recording.ReadIndex(root); len(records) != 0 {
		t.Fatalf("capture entry survived delete: %+v", records)
	}
	if entries := srv.recordingStorage.ListAll(); len(entries) != 1 || entries[0].ID != "mobile" {
		t.Fatalf("capture delete leaked to the mobile index: %+v", entries)
	}
}

func TestCaptureRecordingRecoversPendingDownload(t *testing.T) {
	srv, _ := newTestServer(t, "")
	root := withCaptureRecordings(t, srv)
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("recovered"))
	}))
	defer audio.Close()
	if _, err := srv.captureRecordingStorage.Ingest("recover", recording.Metadata{
		OssAudioURL: audio.URL + "/recover.m4a",
	}, "wuying-a"); err != nil {
		t.Fatal(err)
	}
	srv.captureRecordingStorage = recording.NewCaptureStorage(root, srv.logger)
	if err := srv.captureRecordingStorage.Init(); err != nil {
		t.Fatal(err)
	}
	status := &recordingStatusCapture{events: make(chan recording.StatusEvent, 1)}
	srv.egress = status
	if count := srv.recoverRecordingAudio(); count != 1 {
		t.Fatalf("recover count = %d", count)
	}
	select {
	case event := <-status.events:
		if event.RecordingID != "recover" || event.SourceType != recording.SourceCaptureApp || event.AudioStatus != recording.AudioStatusDownloaded {
			t.Fatalf("incorrect recovery status: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing recovery status")
	}
}

func TestConcurrentRecordingWritesCannotClaimBothSources(t *testing.T) {
	srv, _ := newTestServer(t, "")
	withRecordings(t, srv)
	withCaptureRecordings(t, srv)
	responses := make(chan *httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	for _, source := range []string{recording.SourceCaptureApp, recording.SourceMobileApp} {
		go func() {
			<-start
			body := fmt.Sprintf(`{"recordingId":"same-id","sourceType":%q,"summary":{"markdown":"result"}}`, source)
			req := httptest.NewRequest(http.MethodPost, "/gateway/recordings.result.write", strings.NewReader(body))
			resp := httptest.NewRecorder()
			srv.ServeHTTP(resp, req)
			responses <- resp
		}()
	}
	close(start)
	codes := make([]string, 0, 2)
	for range 2 {
		var reply struct {
			OK    bool
			Error struct{ Code string }
		}
		if err := json.Unmarshal((<-responses).Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.OK {
			codes = append(codes, "OK")
		} else {
			codes = append(codes, reply.Error.Code)
		}
	}
	slices.Sort(codes)
	if !slices.Equal(codes, []string{"OK", "SOURCE_CONFLICT"}) ||
		len(srv.recordingStorage.ListAll())+len(srv.captureRecordingStorage.ListAll()) != 1 {
		t.Fatalf("expected only one source to claim the ID: %v", codes)
	}
}

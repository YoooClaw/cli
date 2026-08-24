package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
)

// asClient 发一个带 Relay 隧道身份的请求；label 为空表示本机 loopback（不隔离）。
func asClient(t *testing.T, method, url, label, body string) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if label != "" {
		req.Header.Set(relay.InternalHTTPHeader, "1")
		req.Header.Set(relay.InternalClientLabelHeader, label)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s %s: %v", method, url, err)
	}
	return out
}

func gatewayData(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("gateway call failed: %+v", resp)
	}
	data, _ := resp["data"].(map[string]any)
	return data
}

func recordingIDs(t *testing.T, resp map[string]any) []string {
	t.Helper()
	raw, _ := gatewayData(t, resp)["recordings"].([]any)
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		id, _ := item.(map[string]any)["recordingId"].(string)
		ids = append(ids, id)
	}
	return ids
}

func withRecordings(t *testing.T, srv *server) *recording.Storage {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "recordings")
	storage := recording.NewStorage(dir, srv.logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	srv.recordingStorage = storage
	srv.recordingEventLog = recording.NewEventLog(dir)
	srv.egress = NoopEgress{}
	return storage
}

// TestRecordingReadScopedByClientLabel：手机 A 只看得到自己的录音 + 来源不明的
// 历史数据，看不到手机 B 的；本机 loopback 看全量。
func TestRecordingReadScopedByClientLabel(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	for id, label := range map[string]string{"rec_a": "phone-a", "rec_b": "phone-b", "rec_old": ""} {
		if _, err := storage.Ingest(id, recording.Metadata{Name: id}, label); err != nil {
			t.Fatal(err)
		}
	}

	listURL := ts.URL + "/gateway/recordings.list"
	got := recordingIDs(t, asClient(t, http.MethodPost, listURL, "phone-a", `{}`))
	want := map[string]bool{"rec_a": true, "rec_old": true}
	if len(got) != 2 {
		t.Fatalf("phone-a sees %v, want rec_a + rec_old", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("phone-a sees %v, want rec_a + rec_old", got)
		}
	}
	if local := recordingIDs(t, asClient(t, http.MethodPost, listURL, "", `{}`)); len(local) != 3 {
		t.Fatalf("loopback sees %v, want all 3", local)
	}
}

// TestRecordingMutationScopedByClientLabel：别的客户端的录音一律当不存在，
// 读、改名、删除、重转写都不能越界。
func TestRecordingMutationScopedByClientLabel(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	if _, err := storage.Ingest("rec_b", recording.Metadata{Name: "B 的录音"}, "phone-b"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ method, body string }{
		{"recordings.status", `{"recordingId":"rec_b"}`},
		{"recordings.rename", `{"recordingId":"rec_b","name":"改个名"}`},
		{"recordings.delete", `{"recordingId":"rec_b"}`},
		{"recordings.retranscribe", `{"recordingId":"rec_b","asr":{"mode":"api"}}`},
	} {
		resp := asClient(t, http.MethodPost, ts.URL+"/gateway/"+c.method, "phone-a", c.body)
		errBody, _ := resp["error"].(map[string]any)
		if ok, _ := resp["ok"].(bool); ok || errBody["code"] != "NOT_FOUND" {
			t.Errorf("%s as phone-a = %+v, want NOT_FOUND", c.method, resp)
		}
	}
	if entry, _ := storage.FindByID("rec_b"); entry.Metadata.Name != "B 的录音" {
		t.Errorf("phone-a renamed another client's recording: %+v", entry)
	}
}

// TestWebPageReadScopedByClientLabel：扩展只回填自己收藏的网页。
func TestWebPageReadScopedByClientLabel(t *testing.T) {
	_, ts := newTestServer(t, "")
	pages := map[string]string{"webext-a": "https://a.example.com/x", "webext-b": "https://b.example.com/y"}
	for label, url := range pages {
		body := `{"canonicalUrl":"` + url + `","url":"` + url + `","title":"t","markdown":"# t"}`
		resp := asClient(t, http.MethodPost, ts.URL+"/web-pages", label, body)
		if ok, _ := resp["ok"].(bool); !ok {
			t.Fatalf("ingest as %s failed: %+v", label, resp)
		}
	}

	indexed := func(label string) []any {
		resp := asClient(t, http.MethodGet, ts.URL+"/web-pages/index", label, "")
		out, _ := resp["pages"].([]any)
		return out
	}
	if got := indexed("webext-a"); len(got) != 1 {
		t.Fatalf("webext-a index = %+v, want only its own page", got)
	}
	if got := indexed(""); len(got) != 2 {
		t.Fatalf("loopback index = %+v, want both pages", got)
	}

	// status 走同一份可见性判定：别人的 hash 不能回「已收藏」。
	hash := indexed("webext-b")[0].(map[string]any)["urlHash"].(string)
	resp := asClient(t, http.MethodGet, ts.URL+"/web-pages/status?h="+hash, "webext-a", "")
	saved, _ := resp["saved"].(map[string]any)
	if len(saved) != 0 {
		t.Errorf("webext-a sees webext-b's page as saved: %+v", saved)
	}
	resp = asClient(t, http.MethodGet, ts.URL+"/web-pages/status?h="+hash, "webext-b", "")
	if saved, _ = resp["saved"].(map[string]any); len(saved) != 1 {
		t.Errorf("webext-b lost its own page: %+v", resp)
	}
}

type recordingEgress struct {
	broadcast []string
	targeted  map[string]int
}

func (e *recordingEgress) PushEvent(event string, _ any) error {
	e.broadcast = append(e.broadcast, event)
	return nil
}

func (e *recordingEgress) PushEventTo(label string, event string, payload any) error {
	if label == "" {
		return e.PushEvent(event, payload)
	}
	if e.targeted == nil {
		e.targeted = map[string]int{}
	}
	e.targeted[label]++
	return nil
}

// TestRecordingStatusEventTargetsOwner：录音状态事件只回给来源客户端，
// 来源不明的历史录音仍旧广播。
func TestRecordingStatusEventTargetsOwner(t *testing.T) {
	srv, _ := newTestServer(t, "")
	storage := withRecordings(t, srv)
	egress := &recordingEgress{}
	srv.egress = egress
	if _, err := storage.Ingest("rec_a", recording.Metadata{Name: "A"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Ingest("rec_old", recording.Metadata{Name: "老录音"}, ""); err != nil {
		t.Fatal(err)
	}

	srv.notifyRecordingStatus(recording.StatusEvent{RecordingID: "rec_a", TransferStatus: recording.StatusTranscribed})
	if egress.targeted["phone-a"] != 1 || len(egress.broadcast) != 0 {
		t.Fatalf("owned recording not targeted: targeted=%+v broadcast=%+v", egress.targeted, egress.broadcast)
	}

	srv.notifyRecordingStatus(recording.StatusEvent{RecordingID: "rec_old", TransferStatus: recording.StatusTranscribed})
	if len(egress.broadcast) != 1 {
		t.Fatalf("legacy recording not broadcast: %+v", egress.broadcast)
	}
}

package daemon

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
)

func postInternalGateway(t *testing.T, url, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(relay.InternalHTTPHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var envelope map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestRecordingListAlwaysReturnsProtocolAndSupportsLimitZero(t *testing.T) {
	srv, ts := newTestServer(t, "")
	dir := filepath.Join(t.TempDir(), "recordings")
	storage := recording.NewStorage(dir, srv.logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	srv.recordingStorage = storage
	if _, err := storage.Ingest("list-1", recording.Metadata{Name: "录音", CreatedAt: "2026-08-27T11:00:00+08:00"}, "default"); err != nil {
		t.Fatal(err)
	}

	envelope := postInternalGateway(t, ts.URL+"/gateway/recordings.list", `{"limit":0}`)
	if envelope["ok"] != true {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	data := envelope["data"].(map[string]any)
	if data["total"] != float64(1) || len(data["recordings"].([]any)) != 0 {
		t.Fatalf("unexpected limit=0 result: %+v", data)
	}
	protocol := data["protocol"].(map[string]any)
	if protocol["orderedWrite"] != float64(1) || protocol["audioOnlyWrite"] != true {
		t.Fatalf("unexpected protocol: %+v", protocol)
	}

	invalid := postInternalGateway(t, ts.URL+"/gateway/recordings.list", `{"limit":null}`)
	if invalid["ok"] != false || invalid["error"].(map[string]any)["code"] != "INVALID_PARAMS" {
		t.Fatalf("null limit must fail: %+v", invalid)
	}
}

func TestOrderedGatewayAcceptsExplicitEmptyFullResultAndIsIdempotent(t *testing.T) {
	srv, ts := newTestServer(t, "")
	dir := filepath.Join(t.TempDir(), "recordings")
	storage := recording.NewStorage(dir, srv.logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	srv.recordingStorage = storage

	base := `{"recordingId":"gateway-ordered","writeRevision":1,"ossUrl":"` + ts.URL + `/audio.m4a","recording":{"name":"录音","duration_sec":0,"created_at":"2026-08-27T11:00:00+08:00","oss_audio_url":"` + ts.URL + `/audio.m4a","markers":[]},"transcript":{"text":""},"summary":{"markdown":""}}`
	envelope := postInternalGateway(t, ts.URL+"/gateway/recordings.result.write", base)
	if envelope["ok"] != true {
		t.Fatalf("ordered empty result rejected: %+v", envelope)
	}
	data := envelope["data"].(map[string]any)
	if data["writeRevision"] != float64(1) || data["transfer_status"] != "transcribed" {
		t.Fatalf("unexpected ordered result: %+v", data)
	}

	retry := postInternalGateway(t, ts.URL+"/gateway/recordings.result.write", base)
	if retry["ok"] != true {
		t.Fatalf("idempotent retry failed: %+v", retry)
	}

	invalidRevision := postInternalGateway(t, ts.URL+"/gateway/recordings.result.write", strings.Replace(base, `"writeRevision":1`, `"writeRevision":null`, 1))
	if invalidRevision["ok"] != false || invalidRevision["error"].(map[string]any)["code"] != "INVALID_PARAMS" {
		t.Fatalf("null revision must fail: %+v", invalidRevision)
	}
}

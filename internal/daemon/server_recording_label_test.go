package daemon

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
)

// TestRecordingResultWriteKeepsRelayClientLabel 回归：录音经 Relay 隧道回环写入时，
// 曾丢掉隧道自带的 client label，全部落到 default（通知走的是同一套鉴权，没这个问题）。
func TestRecordingResultWriteKeepsRelayClientLabel(t *testing.T) {
	srv, ts := newTestServer(t, "")
	dir := filepath.Join(t.TempDir(), "recordings")
	storage := recording.NewStorage(dir, srv.logger)
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	srv.recordingStorage = storage
	srv.recordingEventLog = recording.NewEventLog(dir)
	srv.egress = NoopEgress{}

	const label = "8bf56c19debc4fd587378c88a4f419d8"
	body := `{"recordingId":"rec_1","transcript":{"text":"你好"}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/gateway/recordings.result.write", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(relay.InternalHTTPHeader, "1")
	req.Header.Set(relay.InternalClientLabelHeader, label)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("result.write status = %d", resp.StatusCode)
	}

	entry, ok := storage.FindByID("rec_1")
	if !ok {
		t.Fatal("recording not stored")
	}
	if entry.ClientLabel != label {
		t.Fatalf("clientLabel = %q, want %q", entry.ClientLabel, label)
	}
}

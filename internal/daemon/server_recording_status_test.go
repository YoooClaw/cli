package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/recording"
)

type recordingStatusCapture struct {
	events chan recording.StatusEvent
}

func (c *recordingStatusCapture) PushEvent(event string, payload any) error {
	return c.capture(event, payload)
}

func (c *recordingStatusCapture) PushEventTo(_ string, event string, payload any) error {
	return c.capture(event, payload)
}

func (c *recordingStatusCapture) capture(event string, payload any) error {
	status, ok := payload.(recording.StatusEvent)
	if event == "recording.status" && ok {
		c.events <- status
	}
	return nil
}

func TestRecordingStatusMatchesDownloadedEvent(t *testing.T) {
	srv, ts := newTestServer(t, "")
	storage := withRecordings(t, srv)
	capture := &recordingStatusCapture{events: make(chan recording.StatusEvent, 4)}
	srv.egress = capture

	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("recording-audio"))
	}))
	t.Cleanup(audio.Close)

	const recordingID = "rec_audio_status"
	body, err := json.Marshal(map[string]any{
		"recordingId": recordingID,
		"ossUrl":      audio.URL + "/recording.ogg",
		"transcript":  map[string]any{"text": "测试转录"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeResp := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.result.write", "phone-a", string(body))
	if ok, _ := writeResp["ok"].(bool); !ok {
		t.Fatalf("result.write failed: %+v", writeResp)
	}

	var downloaded recording.StatusEvent
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for downloaded.AudioStatus != recording.AudioStatusDownloaded {
		select {
		case downloaded = <-capture.events:
		case <-timer.C:
			t.Fatal("timed out waiting for downloaded recording.status event")
		}
	}

	statusResp := asClient(t, http.MethodPost, ts.URL+"/gateway/recordings.status", "phone-a",
		`{"recordingId":"`+recordingID+`"}`)
	status := gatewayData(t, statusResp)
	for key, want := range map[string]any{
		"audio_status": downloaded.AudioStatus,
		"audioFile":    downloaded.AudioFile,
		"updatedAt":    downloaded.UpdatedAt,
	} {
		if got := status[key]; got != want {
			t.Errorf("recordings.status %s = %v, want event value %v; response=%+v", key, got, want, status)
		}
	}

	entry, ok := storage.FindByID(recordingID)
	if !ok || entry.AudioStatus != recording.AudioStatusDownloaded {
		t.Fatalf("recording was not persisted as downloaded: %+v", entry)
	}
}

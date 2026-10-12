package update

import (
	"context"
	"encoding/json"
	"github.com/YoooClaw/cli/internal/fsutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportRetryKeepsIDAndRedactsBeforeDisk(t *testing.T) {
	calls := 0
	var reportIDs []string
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Events []Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reportIDs = append(reportIDs, body.Events[0].ReportID)
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0}`))
	})
	a := Attempt{Identity: id, UpdateID: ID(), Trigger: "cli"}
	Record(a, "apply", "failed", fail("TEST", "failure ock-private-key"))
	events := readEvents(id.Directory())
	if len(events) != 1 || strings.Contains(events[0].ErrorMessage, "ock-private-key") {
		t.Fatal(events)
	}
	Flush(context.Background(), c, id)
	if len(readEvents(id.Directory())) != 1 {
		t.Fatal("lost retry event")
	}
	_ = os.Remove(filepath.Join(id.Directory(), "retry.json"))
	Flush(context.Background(), c, id)
	if len(readEvents(id.Directory())) != 0 || len(reportIDs) != 2 || reportIDs[0] != reportIDs[1] {
		t.Fatal(reportIDs)
	}
}
func TestTerminalFirstResultWinsAndUsesUpdateID(t *testing.T) {
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != ResultPath || body["updateId"] == "" || body["errorCode"] != nil {
			t.Error(body)
		}
		_, _ = w.Write([]byte(`{"success":true,"code":"000000","data":true}`))
	})
	a := Attempt{Identity: id, UpdateID: ID(), Artifact: Artifact{TargetVersion: "0.13.0"}}
	Final(a, nil)
	Final(a, fail("LATE", "late failure"))
	Flush(context.Background(), c, id)
	var result terminal
	_, _ = fsutil.ReadJSON(filepath.Join(id.Directory(), a.UpdateID+".result.json"), &result)
	if !result.Retry.Delivered || result.Body["errorCode"] != nil {
		t.Fatal(result)
	}
}
func TestHeartbeatOncePer24HoursAcrossVersions(t *testing.T) {
	id, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	Heartbeat(id)
	id.CurrentVersion = "0.13.0"
	Heartbeat(id)
	if len(readEvents(id.Directory())) != 1 {
		t.Fatal("heartbeat interval reset by version change")
	}
	_ = fsutil.WriteJSON(filepath.Join(id.Directory(), "heartbeat.json"), time.Now().Add(-25*time.Hour), 0600)
	Heartbeat(id)
	if len(readEvents(id.Directory())) != 2 {
		t.Fatal("missing heartbeat")
	}
}

func TestCommandFailureRedactsBeforeReturning(t *testing.T) {
	err := CommandFailure("daemon start", []byte("connection refused, credential ock-fixturesecret"))
	if strings.Contains(err.Error(), "ock-fixturesecret") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatal("diagnostic was leaked or lost")
	}
}

func TestLegacyQueueUsesCanonicalDeviceID(t *testing.T) {
	old := strings.Repeat("a", 32)
	reportID, updateID := ID(), ID()
	calls := 0
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == ReportPath {
			var body struct {
				Events []Event `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Events) != 1 {
				t.Errorf("invalid events: %v", err)
				w.WriteHeader(400)
				return
			}
			e := body.Events[0]
			if e.DeviceID != Hash([]byte(old)) || e.ReportID != reportID || e.UpdateID != updateID {
				t.Error("queue identity changed incorrectly")
			}
			_, _ = w.Write([]byte(`{"accepted":1,"rejected":0}`))
		} else {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["deviceId"] != Hash([]byte(old)) || body["updateId"] != updateID {
				t.Error(body)
			}
			_, _ = w.Write([]byte(`{"success":true,"code":"000000","data":true}`))
		}
	})
	id.DeviceID = old
	a := Attempt{Identity: id, UpdateID: updateID, Artifact: Artifact{TargetVersion: "0.13.0"}}
	e := EventFor(a, "activate", "succeeded", nil)
	if e.DeviceID != Hash([]byte(old)) {
		t.Fatal("old attempt not normalized")
	}
	e.DeviceID = old
	e.ReportID = reportID
	if err := fsutil.WriteJSON(filepath.Join(id.Directory(), reportID+".event.json"), e, 0600); err != nil {
		t.Fatal(err)
	}
	Final(a, nil)
	Flush(context.Background(), c, id)
	if calls != 2 || len(readEvents(id.Directory())) != 0 {
		t.Fatalf("calls=%d", calls)
	}
}

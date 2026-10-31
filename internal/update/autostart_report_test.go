package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func TestAutostartFailurePendingHandoff(t *testing.T) {
	id := Identity{Root: t.TempDir(), Profile: "default", KeyHash: "key", BaseURL: "https://example.com", CurrentVersion: "0.15.0"}
	a := Attempt{Identity: id, UpdateID: ID(), Target: filepath.Join(id.Root, "yoooclaw.exe"), Ready: true, Artifact: Artifact{TargetVersion: "0.15.0"}}
	a.Identity.CurrentVersion = "0.13.2"
	if err := fsutil.WriteJSON(JobPath(a), a, 0600); err != nil {
		t.Fatal(err)
	}
	detail := errors.New("任务被禁用 HRESULT:0x80041326 ock-private-secret")
	wrong := id
	wrong.Profile = "other"
	ReportAutostartFailure(wrong, a.Target, detail)
	ReportAutostartFailure(id, a.Target+".old", detail)
	if len(readEvents(id.Directory())) != 0 {
		t.Fatal("reported unrelated attempt")
	}
	ReportAutostartFailure(id, a.Target, detail)
	events := readEvents(id.Directory())
	if len(events) != 1 {
		t.Fatalf("events: %+v", events)
	}
	e := events[0]
	if e.ErrorCode != "AUTOSTART_START_FAILED" || e.From != "0.13.2" || e.InstalledVersion != "0.15.0" || e.UpdateID != a.UpdateID || !strings.Contains(e.ErrorMessage, "0x80041326") || strings.Contains(e.ErrorMessage, "private-secret") {
		t.Fatalf("%+v", e)
	}
	if _, err := os.Stat(filepath.Join(id.Directory(), a.UpdateID+".result.json")); !os.IsNotExist(err) {
		t.Fatal("recoverable error finalized update", err)
	}
	saved, err := ReadAttempt(JobPath(a))
	if err != nil || !saved.Ready {
		t.Fatal("handoff retired too early", err)
	}
	ConfirmPending(id, a.Target)
	saved, err = ReadAttempt(JobPath(a))
	if err != nil || saved.Ready {
		t.Fatal("activation failed", err)
	}
	n := len(readEvents(id.Directory()))
	ReportAutostartFailure(id, a.Target, detail)
	if len(readEvents(id.Directory())) != n {
		t.Fatal("reported retired attempt")
	}
}

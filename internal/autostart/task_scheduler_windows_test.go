//go:build windows

package autostart

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	ole "github.com/go-ole/go-ole"
)

func TestWindowsTaskSchedulerNativeReadOnlyWithoutShell(t *testing.T) {
	// This would make the old exec.Command("powershell.exe", ...) fail. Do not
	// alter system policy or rename/delete any Windows programs for the test.
	t.Setenv("PATH", t.TempDir())
	for range 3 { // Exercise COM initialization and release across repeated calls.
		if _, err := nativeTaskSchedulerCOM("available"); err != nil {
			t.Fatal(err)
		}
		sid, err := nativeTaskSchedulerCOM("identity")
		if err != nil || !strings.HasPrefix(string(sid), "S-1-") {
			t.Fatalf("identity = %q, %v", sid, err)
		}
		missing := fmt.Sprintf("yoooclaw-missing-test-%d-%d", os.Getpid(), time.Now().UnixNano())
		out, err := nativeTaskSchedulerCOM("status", `\`, missing)
		if err != nil || string(out) != "missing" {
			t.Fatalf("missing task = %q, %v", out, err)
		}
	}
}

func TestWindowsTaskSchedulerNativeErrorKeepsHRESULT(t *testing.T) {
	for _, code := range []uint32{0x80070002, 0x80070005, 0x800704EC, 0x800706BA} {
		cause := ole.NewError(uintptr(code))
		err := nativeTaskSchedulerError("GetTask", cause)
		var got *taskSchedulerError
		if !errors.As(err, &got) || got.hresult != code || !errors.Is(err, cause) {
			t.Fatalf("HRESULT %08X was lost: %v", code, err)
		}
		if taskSchedulerNotFound(err) != (code == 0x80070002) {
			t.Fatalf("unexpected not-found classification: %v", err)
		}
	}
}

// Registration-only smoke test: keep the legacy launcher, do not start a daemon.
func TestWindowsTaskSchedulerNativeRegistration(t *testing.T) {
	if os.Getenv("YOOOCLAW_TASK_SCHEDULER_INTEGRATION") != "1" {
		t.Skip("opt-in isolated Task Scheduler registration test")
	}
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	m := &platformManager{root: root, task: fmt.Sprintf(`\yoooclaw-native-test-%d-%d`, os.Getpid(), time.Now().UnixNano())}
	t.Cleanup(func() {
		if err := m.Uninstall(); err != nil {
			t.Errorf("cleanup test task: %v", err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{RootDir: root, Executable: exe, Arguments: []string{"--help"}}
	for range 2 {
		if err := m.Install(spec); err != nil {
			t.Fatal(err)
		}
		status, err := m.Status()
		if err != nil || !status.Installed || status.Running {
			t.Fatalf("cold registered task = %+v, %v", status, err)
		}
	}
}

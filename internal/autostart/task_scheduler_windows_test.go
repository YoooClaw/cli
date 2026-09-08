//go:build windows

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// Opt-in because it registers and runs a real current-user task. CI enables
// this only on its disposable Windows runner. No existing CLI task is touched.
func TestWindowsTaskSchedulerNativeLifecycle(t *testing.T) {
	if os.Getenv("YOOOCLAW_TASK_SCHEDULER_INTEGRATION") != "1" {
		t.Skip("set YOOOCLAW_TASK_SCHEDULER_INTEGRATION=1 for isolated native task lifecycle test")
	}
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	m := &platformManager{
		root: root,
		task: fmt.Sprintf(`\yoooclaw-native-test-%d-%d`, os.Getpid(), time.Now().UnixNano()),
	}
	t.Cleanup(func() {
		if err := m.Uninstall(); err != nil {
			t.Errorf("cleanup test task %s: %v", m.task, err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "started.pid")
	spec := Spec{
		RootDir: root, Executable: exe,
		Arguments: []string{"-test.run=^TestWindowsTaskSchedulerNativeHelper$", "--", "yoooclaw-native-task-helper", marker},
	}
	for range 2 { // Both create and update an existing task with the same identity.
		if err := m.Install(spec); err != nil {
			t.Fatal(err)
		}
		status, err := m.Status()
		if err != nil || !status.Installed || !status.Loaded || status.Running {
			t.Fatalf("cold registered task = %+v, %v", status, err)
		}
	}
	// Exercise the same delayed native trigger used by an Agent. There is no
	// PowerShell, script host, Task.Run or long-lived Agent child involved.
	if _, err := m.Schedule(spec, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	var pidText []byte
	for time.Now().Before(deadline) {
		pidText, err = os.ReadFile(marker)
		if err == nil && len(pidText) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	pid, err := strconv.Atoi(string(pidText))
	if err != nil || pid <= 0 {
		t.Fatalf("task did not launch hidden helper, marker = %q: %v", pidText, err)
	}
	process, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(process)
	status, err := m.Status()
	if err != nil || !status.Running {
		t.Fatalf("started task = %+v, %v", status, err)
	}
	inspection, err := m.Inspect(spec, pid)
	for !inspection.ManagedDaemonVerified && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		inspection, err = m.Inspect(spec, pid)
	}
	if err != nil || !inspection.DefinitionMatches || !inspection.ManagedDaemonVerified || inspection.DaemonPID != pid {
		t.Fatalf("native process association=%+v err=%v", inspection, err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if state, err := syscall.WaitForSingleObject(process, 5000); err != nil || state != syscall.WAIT_OBJECT_0 {
		t.Fatalf("task stopped but its helper survived: state=%d, err=%v", state, err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status()
	if err != nil || status.Installed || status.Loaded || status.Running {
		t.Fatalf("deleted task = %+v, %v", status, err)
	}
}

func TestWindowsTaskSchedulerNativeHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "yoooclaw-native-task-helper" && i+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				t.Fatal(err)
			}
			// Finite lifetime even if Task Scheduler Stop fails in the parent test.
			time.Sleep(30 * time.Second)
			return
		}
	}
	t.Skip("only run as the isolated Task Scheduler test action")
}

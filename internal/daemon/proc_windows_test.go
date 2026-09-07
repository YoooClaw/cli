//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"testing"
)

func TestWindowsDetachedDaemonDoesNotInheritParentConsole(t *testing.T) {
	attr := detachSysProcAttr()
	if attr == nil {
		t.Fatal("detachSysProcAttr returned nil")
	}
	if attr.CreationFlags&windowsCreateNewProcessGroup == 0 {
		t.Fatal("daemon is not the root of a new process group")
	}
	if attr.CreationFlags&windowsDetachedProcess == 0 {
		t.Fatal("daemon still inherits the parent console")
	}
	if !attr.HideWindow {
		t.Fatal("detached daemon may open a console window")
	}
}

func TestWindowsProcessAliveRejectsExitedProcess(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if isProcessAlive(cmd.Process.Pid) {
		t.Fatalf("exited process %d reported alive", cmd.Process.Pid)
	}
}

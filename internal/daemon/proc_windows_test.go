//go:build windows

package daemon

import "testing"

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

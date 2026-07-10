//go:build linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

// isExpectedDaemonProcess rejects stale daemon locks that happen to reference
// a live PID. This matters especially in WSL, where the lock survives a distro
// restart while the Linux PID namespace starts over and quickly reuses PIDs.
func isExpectedDaemonProcess(lock *Lock) bool {
	procRoot := fmt.Sprintf("/proc/%d", lock.PID)
	stat, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		// /proc can be hidden by container policy. Preserve the historical
		// signal-0 behavior when identity metadata is unavailable.
		return true
	}
	if linuxProcessState(stat) == 'Z' || linuxProcessState(stat) == 'X' {
		return false
	}
	if lock.Executable == "" {
		return true // legacy locks did not record enough identity to verify.
	}

	actualExecutable, err := os.Readlink(filepath.Join(procRoot, "exe"))
	if err == nil && !sameExecutable(lock.Executable, actualExecutable) {
		return false
	}
	cmdline, err := os.ReadFile(filepath.Join(procRoot, "cmdline"))
	if err == nil && len(cmdline) > 0 && !isDaemonCommandLine(cmdline) {
		return false
	}
	return true
}

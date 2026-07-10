//go:build !linux

package daemon

// Non-Linux platforms do not expose the /proc identity data used to reject PID
// reuse. Their existing platform-specific process liveness check remains the
// best available signal.
func isExpectedDaemonProcess(_ *Lock) bool { return true }

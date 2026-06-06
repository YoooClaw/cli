//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

// isProcessAlive 用 signal 0 探活：nil=存活；EPERM=存在但无权限（仍算存活）；ESRCH=不存在。
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

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

// detachSysProcAttr 让 fork 出的 daemon 子进程脱离当前会话（独立进程组）。
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminate 向进程发送 SIGTERM（优雅）。
func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// forceKill 向进程发送 SIGKILL（强杀）。
func forceKill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

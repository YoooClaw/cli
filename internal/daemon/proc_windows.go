//go:build windows

package daemon

import (
	"os"
	"syscall"
)

// isProcessAlive 在 Windows 上 best-effort：FindProcess 成功即视为存活。
// （daemon 主要运行于 unix；Windows 探活精度有限，Phase 2 视需要再加固。）
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

// detachSysProcAttr 在 Windows 上新建进程组以脱离父进程。
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

// terminate / forceKill：Windows 无 POSIX 信号，统一调用 Kill（TerminateProcess）。
func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func forceKill(pid int) error { return terminate(pid) }

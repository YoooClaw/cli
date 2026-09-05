//go:build windows

package daemon

import (
	"os"
	"syscall"
)

const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsDetachedProcess       = 0x00000008
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

// detachSysProcAttr 在 Windows 上同时脱离父进程的控制台并新建进程组。
// 只创建新进程组仍会继承父控制台；WorkBuddy/Trae 等宿主关闭会话时发送的
// console close 信号会因此带走后台 daemon。
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow: true,
		CreationFlags: windowsCreateNewProcessGroup |
			windowsDetachedProcess,
	}
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

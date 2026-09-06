//go:build windows

package daemon

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsDetachedProcess       = 0x00000008
	windowsProcessQueryLimited   = 0x00001000
	windowsStillActive           = 259
)

var (
	windowsKernel32Proc       = syscall.NewLazyDLL("kernel32.dll")
	windowsGetExitCodeProcess = windowsKernel32Proc.NewProc("GetExitCodeProcess")
)

// isProcessAlive reads the Windows process exit code. os.FindProcess alone is
// insufficient because it also succeeds for a PID whose process has exited.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(windowsProcessQueryLimited, false, uint32(pid))
	if err != nil {
		// Access denied is not proof that the process is gone. Stay conservative
		// so lifecycle code never starts a second daemon over an unknown owner.
		return !errors.Is(err, syscall.Errno(87)) // ERROR_INVALID_PARAMETER
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	result, _, _ := windowsGetExitCodeProcess.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&exitCode)),
	)
	return result != 0 && exitCode == windowsStillActive
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

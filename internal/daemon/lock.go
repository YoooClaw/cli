// Package daemon 实现守护进程：进程锁、HTTP server、Relay 隧道装配。
//
// Phase 1 仅提供锁的读取面（status / doctor 需要探活）；写入面与主循环在 Phase 2。
package daemon

import (
	"os"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

// Lock 是 daemon.lock 的内容。
type Lock struct {
	PID        int    `json:"pid"`
	StartedAt  string `json:"startedAt"`
	Bind       string `json:"bind"`
	Port       int    `json:"port"`
	LogLevel   string `json:"logLevel,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Generation string `json:"generation,omitempty"`
	Executable string `json:"executable,omitempty"`
	Version    string `json:"version,omitempty"`
	Profile    string `json:"profile,omitempty"`
}

// RunningState 是 daemon 运行态。
type RunningState struct {
	Running bool
	Lock    *Lock
	// Stale 锁存在但进程已死。
	Stale bool
}

// ReadLock 读取锁文件；不存在返回 nil。
func ReadLock(p paths.Paths) *Lock {
	var lock Lock
	exists, err := fsutil.ReadJSON(p.DaemonLock, &lock)
	if err != nil || !exists {
		return nil
	}
	return &lock
}

// State 返回 daemon 运行态（signal 0 + Linux/WSL /proc 身份校验；陈旧锁视为未运行）。
func State(p paths.Paths) RunningState {
	lock := ReadLock(p)
	if lock == nil {
		return RunningState{}
	}
	// signal 0 alone is not enough on Linux/WSL: it also succeeds for zombies,
	// and a persisted lock can point at an unrelated process after PID reuse.
	// When /proc metadata is available, verify that the PID still belongs to the
	// daemon executable and command line recorded by the lock.
	alive := isProcessAlive(lock.PID) && isExpectedDaemonProcess(lock)
	return RunningState{Running: alive, Lock: lock, Stale: !alive}
}

// WriteLock 写锁文件。
func WriteLock(p paths.Paths, lock Lock) error {
	return fsutil.WriteJSON(p.DaemonLock, lock, fsutil.ConfigFileMode)
}

// RemoveLock 删除锁文件。
func RemoveLock(p paths.Paths) {
	_ = os.Remove(p.DaemonLock)
}

// IsProcessAlive 导出探活（供 daemon 命令判断目标进程）。
func IsProcessAlive(pid int) bool { return isProcessAlive(pid) }

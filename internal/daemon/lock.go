// Package daemon 实现守护进程：进程锁、HTTP server、Relay 隧道装配。
//
// Phase 1 仅提供锁的读取面（status / doctor 需要探活）；写入面与主循环在 Phase 2。
package daemon

import (
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

// Lock 是 daemon.lock 的内容。
type Lock struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
	Bind      string `json:"bind"`
	Port      int    `json:"port"`
	LogLevel  string `json:"logLevel,omitempty"`
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

// State 返回 daemon 运行态（通过 signal 0 探活；陈旧锁视为未运行）。
func State(p paths.Paths) RunningState {
	lock := ReadLock(p)
	if lock == nil {
		return RunningState{}
	}
	alive := isProcessAlive(lock.PID)
	return RunningState{Running: alive, Lock: lock, Stale: !alive}
}

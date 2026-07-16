//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// acquireProcessLock 非阻塞获取排他 flock 并持有到 release 被调用（或进程退出）。
// 已被其他进程持有时返回 errProcessLockHeld。
func acquireProcessLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errProcessLockHeld
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

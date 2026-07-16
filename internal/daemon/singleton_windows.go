//go:build windows

package daemon

import (
	"os"
	"syscall"
	"unsafe"
)

// acquireProcessLock 非阻塞获取字节区间锁 [0,1) 并持有到 release 被调用（或进程退出）。
// 已被其他进程持有时返回 errProcessLockHeld。与 writerlock_windows.go 共用
// kernel32 的 LockFileEx/UnlockFileEx。
func acquireProcessLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	ret, _, callErr := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		_ = f.Close()
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorLockViolation {
			return nil, errProcessLockHeld
		}
		return nil, callErr
	}
	return func() {
		var ov syscall.Overlapped
		_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
		_ = f.Close()
	}, nil
}

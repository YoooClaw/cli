//go:build windows

package daemon

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errorLockViolation      = 33 // ERROR_LOCK_VIOLATION
)

// probeFileLock 非阻塞探测字节区间锁 [0,1)：与插件侧 writer_lock.py 的
// msvcrt.locking(LK_NBLCK, 1) 同一锁命名空间（底层同为 LockFile 区间锁）。
func probeFileLock(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	var overlapped syscall.Overlapped
	ret, _, callErr := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorLockViolation {
			return true, nil
		}
		return false, callErr
	}
	_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	return false, nil
}

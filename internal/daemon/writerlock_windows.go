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

	// Hermes writer_lock.py locks this sentinel byte so the JSON owner/PID
	// metadata at offset 0 remains readable while the mandatory Windows lock
	// is held. Keep this value in sync with _WINDOWS_LOCK_OFFSET there.
	windowsWriterLockOffset = 0x7FFF_FFFF
)

// probeFileLock 非阻塞探测字节区间锁 [0,1)。daemon singleton 与 account
// Relay consumer 继续使用这个范围；不能为了 writer lock 改动它。
func probeFileLock(path string) (bool, error) {
	return probeFileLockAt(path, 0)
}

// probeWriterFileLock 探测 Hermes 插件持有的 writer sentinel byte。
// Python msvcrt.locking 与 Windows LockFileEx 共用字节区间锁命名空间。
func probeWriterFileLock(path string) (bool, error) {
	return probeFileLockAt(path, windowsWriterLockOffset)
}

func probeFileLockAt(path string, offset uint32) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	overlapped := syscall.Overlapped{Offset: offset}
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

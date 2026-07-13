//go:build !windows

package daemon

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// probeFileLock 非阻塞探测 flock：拿得到（随即释放）或文件不存在 → 未被持有。
// 与插件侧 writer_lock.py 的 fcntl.flock(LOCK_EX) 同一锁命名空间。
func probeFileLock(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

//go:build darwin || linux

package fsutil

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// RequireSpace checks the filesystem of the nearest existing parent, with a 64 MiB reserve.
func RequireSpace(path string, bytes int64) error {
	for {
		if _, e := os.Stat(path); e == nil {
			break
		} else if !os.IsNotExist(e) {
			return e
		}
		parent := filepath.Dir(path)
		if parent == path {
			return fmt.Errorf("cannot locate target filesystem")
		}
		path = parent
	}
	var st unix.Statfs_t
	if e := unix.Statfs(path, &st); e != nil {
		return e
	}
	if bytes < 0 || uint64(bytes)+(64<<20) > uint64(st.Bavail)*uint64(st.Bsize) {
		return fmt.Errorf("insufficient disk space")
	}
	return nil
}

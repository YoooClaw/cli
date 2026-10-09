//go:build !windows

package transfer

import (
	"os"
	"syscall"
)

// linkCount 返回文件的硬链接数；取不到时按 1 处理。
func linkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}

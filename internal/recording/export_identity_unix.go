//go:build darwin || linux

package recording

import (
	"os"
	"syscall"
)

func exportFileIdentity(info os.FileInfo) (uint64, uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}

//go:build windows

package daemon

import (
	"errors"
	"syscall"
)

// wsaeaddrinuse 是 Windows 的 WSAEADDRINUSE（10048），stdlib syscall 没有导出它。
const wsaeaddrinuse = syscall.Errno(10048)

func isAddrInUse(err error) bool {
	return errors.Is(err, wsaeaddrinuse)
}

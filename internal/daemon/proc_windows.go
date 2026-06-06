//go:build windows

package daemon

import "os"

// isProcessAlive 在 Windows 上 best-effort：FindProcess 成功即视为存活。
// （daemon 主要运行于 unix；Windows 探活精度有限，Phase 2 视需要再加固。）
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

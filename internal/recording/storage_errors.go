package recording

import (
	"errors"
	"strings"
	"syscall"
)

// storageErrorCode 只把明确的本地空间不足映射为 STORAGE_FULL；权限、网络和
// OSS 错误保持未分类，避免错误提示被误导。
func storageErrorCode(err error) string {
	if isStorageFullError(err) {
		return "STORAGE_FULL"
	}
	return ""
}

func isStorageFullError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"no space left", "not enough space", "insufficient disk space", "disk full",
		"error_disk_full", "error_handle_disk_full", "quota exceeded", "enospc", "edquot",
		"磁盘空间不足", "空间不足",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

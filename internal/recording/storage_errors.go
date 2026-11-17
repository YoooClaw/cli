package recording

import (
	"errors"
	"strings"
	"syscall"
)

// StorageFullMessage 是结构化空间不足错误统一的对外文案。客户端必须按
// error.code=STORAGE_FULL 处理，不要匹配这段文本。
const StorageFullMessage = "Not enough disk space"

// IsStorageFull 报告 err 是否为明确的本地空间不足（Unix ENOSPC/EDQUOT 及
// 平台等价错误）。权限、网络、OSS 容量和一般 I/O 错误都不算，避免把其它
// 故障误报成空间不足。
func IsStorageFull(err error) bool { return isStorageFullError(err) }

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

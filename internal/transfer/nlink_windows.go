//go:build windows

package transfer

import "os"

// linkCount 在 Windows 上不区分硬链接（FileInfo 不带链接数），按 1 处理。
func linkCount(os.FileInfo) uint64 { return 1 }

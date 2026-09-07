//go:build !windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
)

func removeNativeSelfBinary(exe string) (binaryRemovalResult, error) {
	result := binaryRemovalResult{Removed: []string{}}
	real := exe
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		real = resolved
	}
	dir := filepath.Dir(real)
	candidates := []string{real, exe, filepath.Join(dir, "yc"), filepath.Join(dir, "yoooclaw")}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		// 只删 native 安装器创建的文件名，避免可执行文件被改名或嵌套时
		// 误删无关文件。
		if base := filepath.Base(candidate); base != "yc" && base != "yoooclaw" {
			continue
		}
		if _, err := os.Lstat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, err
		}
		if err := os.Remove(candidate); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, candidate)
	}
	return result, nil
}

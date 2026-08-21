package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveExistingRegularFile 把相对 root 的路径解析为当前存在的普通文件。
// 绝对路径、目录穿越和符号链接逃逸都会被拒绝。
func ResolveExistingRegularFile(root, relative string) (string, bool) {
	relative = strings.TrimSpace(relative)
	if relative == "" || filepath.IsAbs(relative) {
		return "", false
	}
	cleaned := filepath.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", false
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidate, err := filepath.Abs(filepath.Join(rootAbs, cleaned))
	if err != nil || !pathWithinRoot(rootAbs, candidate) {
		return "", false
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", false
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil || !pathWithinRoot(resolvedRoot, resolvedCandidate) {
		return "", false
	}
	info, err := os.Stat(resolvedCandidate)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolvedCandidate, true
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

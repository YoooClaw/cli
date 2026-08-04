package voice

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveAudioPath(root, relative string) (string, bool) {
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
	if err != nil || !pathWithin(rootAbs, candidate) {
		return "", false
	}

	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", false
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil || !pathWithin(resolvedRoot, resolvedCandidate) {
		return "", false
	}
	info, err := os.Stat(resolvedCandidate)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolvedCandidate, true
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

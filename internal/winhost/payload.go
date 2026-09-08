// Package winhost distributes the native GUI-subsystem daemon host. Release
// builds embed it; ordinary development builds must supply a sibling host.
package winhost

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func Bytes() ([]byte, error) {
	if len(payload) != 0 {
		return payload, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "yoooclaw-host.exe"))
	if err != nil {
		return nil, fmt.Errorf("原生 Windows host 未包含在此开发构建中；请使用 scripts/build-go.sh 构建: %w", err)
	}
	return b, nil
}

// Use a content-addressed name so upgrading never overwrites a running image.
func Path(root string) (string, error) {
	b, err := Bytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return filepath.Join(root, "hosts", fmt.Sprintf("yoooclaw-host-%x.exe", sum[:8])), nil
}

func Ensure(root string) (string, error) {
	b, err := Bytes()
	if err != nil {
		return "", err
	}
	path, err := Path(root)
	if err != nil {
		return "", err
	}
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, b) {
		return path, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := fsutil.WriteAtomic(path, b, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

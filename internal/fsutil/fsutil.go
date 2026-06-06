// Package fsutil 统一目录/文件权限与原子写（对齐 TS 版 src/fs-utils.ts）。
//
// 安全约束：目录 0700、敏感文件 0600；写入走「临时文件 + rename」原子替换。
package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/errs"
)

const (
	// DirMode 目录默认权限（私有）。
	DirMode os.FileMode = 0o700
	// SecretFileMode 敏感文件权限（仅属主可读写）。
	SecretFileMode os.FileMode = 0o600
	// ConfigFileMode 普通配置文件权限。
	ConfigFileMode os.FileMode = 0o644
)

// EnsureDir 确保目录存在并尽力收紧权限（Windows 上 chmod 基本 no-op）。
func EnsureDir(dir string, mode os.FileMode) error {
	if mode == 0 {
		mode = DirMode
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	_ = os.Chmod(dir, mode) // Windows / 受限 FS 忽略
	return nil
}

func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WriteAtomic 原子写文件：先写临时文件再 rename，最后 chmod。
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = ConfigFileMode
	}
	dir := filepath.Dir(path)
	if err := EnsureDir(dir, DirMode); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp", randSuffix()))
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(path, mode)
	return nil
}

// WriteJSON 写 JSON（两空格缩进 + 末尾换行）。
func WriteJSON(path string, v any, mode os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return WriteAtomic(path, data, mode)
}

// ReadJSON 读 JSON 到 out；文件不存在返回 (false, nil)；解析失败返回 CONFIG_INVALID。
func ReadJSON(path string, out any) (exists bool, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, errs.New(errs.CodeConfigInvalid, "读取文件失败："+path)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return true, errs.New(errs.CodeConfigInvalid, "文件不是合法 JSON："+path).
			WithHint("可手动修复或删除后重新生成")
	}
	return true, nil
}

// Exists 报告路径是否存在。
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

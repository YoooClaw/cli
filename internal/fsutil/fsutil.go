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

// CopyDir 递归复制 src 到 dst（保留文件权限位）。
func CopyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, DirMode)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), DirMode); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// CountFiles 递归统计 dir 下的条目数（含目录，对齐 TS readdirSync recursive 语义）。
func CountFiles(dir string) int {
	count := 0
	_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path != dir {
			count++
		}
		return nil
	})
	return count
}

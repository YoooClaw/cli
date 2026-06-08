// Package testutil 汇集单元测试公用的沙箱、假日志与 golden 帮手。
//
// 设计目标：每个测试自带隔离环境，不触碰真实 ~/.yoooclaw、不连网、不调系统钥匙串。
// 仅供 *_test.go 使用（依赖 testing），不要被生产代码 import。
package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

// Sandbox 把 YOOOCLAW_HOME 指向一个临时目录，并预建好 default profile 目录。
// 返回该 profile 的 Paths。t.Setenv 会在测试结束时自动还原环境变量。
//
//	p := testutil.Sandbox(t)
//	config.Save(p, cfg)   // 写入临时目录，互不干扰
func Sandbox(t *testing.T) paths.Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	p := paths.For(paths.DefaultProfile)
	if err := fsutil.EnsureDir(p.Dir, fsutil.DirMode); err != nil {
		t.Fatalf("testutil.Sandbox: 创建 profile 目录失败: %v", err)
	}
	return p
}

// SandboxProfile 同 Sandbox，但解析指定 profile（多 profile 场景）。
func SandboxProfile(t *testing.T, profile string) paths.Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	p := paths.For(profile)
	if err := fsutil.EnsureDir(p.Dir, fsutil.DirMode); err != nil {
		t.Fatalf("testutil.SandboxProfile: 创建 profile 目录失败: %v", err)
	}
	return p
}

// Logger 是把日志转发到 t.Log 的假实现，满足各包 Info/Warn/Error(string) 日志接口。
// 用法：testutil.Logger{T: t}
//
// 取代 recording / relay 等包各自重复定义的 testLogger。
type Logger struct{ T *testing.T }

func (l Logger) Info(msg string)  { l.T.Helper(); l.T.Log("[INFO] " + msg) }
func (l Logger) Warn(msg string)  { l.T.Helper(); l.T.Log("[WARN] " + msg) }
func (l Logger) Error(msg string) { l.T.Helper(); l.T.Log("[ERROR] " + msg) }

// WriteFile 在沙箱里写一个文件（自动建父目录），失败即 t.Fatal。便于摆放 fixture。
func WriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), fsutil.DirMode); err != nil {
		t.Fatalf("testutil.WriteFile: mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, fsutil.SecretFileMode); err != nil {
		t.Fatalf("testutil.WriteFile: %v", err)
	}
}

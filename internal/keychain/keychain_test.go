package keychain

import (
	"runtime"
	"testing"
)

func TestHasCommand(t *testing.T) {
	t.Parallel()
	// "go" 一定存在于运行测试的环境。
	if !hasCommand("go") {
		t.Error("hasCommand(go) should be true in a Go test env")
	}
	if hasCommand("definitely-not-a-real-command-xyz") {
		t.Error("nonexistent command should report false")
	}
}

func TestAvailable(t *testing.T) {
	t.Parallel()
	// 仅断言不 panic 且与平台逻辑一致；具体真假取决于运行环境。
	got := Available()
	switch runtime.GOOS {
	case "darwin":
		if got != hasCommand("security") {
			t.Errorf("darwin Available mismatch: %v", got)
		}
	case "linux":
		if got != hasCommand("secret-tool") {
			t.Errorf("linux Available mismatch: %v", got)
		}
	default:
		if got {
			t.Errorf("non darwin/linux should be unavailable, got %v", got)
		}
	}
}

func TestGetNonexistentIsSafe(t *testing.T) {
	t.Parallel()
	// 读一个极不可能存在的条目：只读、不写，安全。
	res := Get("yoooclaw-unit-test-service-nope", "no-such-account-xyz")
	if res.Value != "" {
		t.Errorf("nonexistent entry should yield empty value, got %q", res.Value)
	}
	// Available 字段应与平台 keychain 工具是否存在一致（不强断真假）。
	_ = res.Available
}

// Package keychain 适配 OS keychain（可选加固，默认不强制）。
//
//   - macOS：security（generic password）
//   - Linux：secret-tool（libsecret / Secret Service）
//   - Windows：暂无可明文取回的内置 CLI → 视为不可用，调用方降级到文件
//
// 任何工具缺失 / 调用失败都返回不可用，绝不崩溃，保证 CI 无 keychain 时的确定性。
package keychain

import (
	"os/exec"
	"runtime"
	"strings"
)

// Result 是 keychain 读取结果。
type Result struct {
	Available bool
	Value     string
}

func hasCommand(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// Available 报告当前平台 keychain 是否可用（工具存在）。
func Available() bool {
	switch runtime.GOOS {
	case "darwin":
		return hasCommand("security")
	case "linux":
		return hasCommand("secret-tool")
	default:
		return false
	}
}

// Get 读取 keychain 条目；不可用或未命中返回 Value=""。
func Get(service, account string) Result {
	switch runtime.GOOS {
	case "darwin":
		if !hasCommand("security") {
			return Result{Available: false}
		}
		out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
		if err != nil {
			return Result{Available: true}
		}
		return Result{Available: true, Value: strings.TrimRight(string(out), "\n")}
	case "linux":
		if !hasCommand("secret-tool") {
			return Result{Available: false}
		}
		out, err := exec.Command("secret-tool", "lookup", "service", service, "account", account).Output()
		if err != nil {
			return Result{Available: true}
		}
		return Result{Available: true, Value: strings.TrimRight(string(out), "\n")}
	default:
		return Result{Available: false}
	}
}

// Set 写入 keychain 条目；返回是否成功。
func Set(service, account, value string) bool {
	switch runtime.GOOS {
	case "darwin":
		if !hasCommand("security") {
			return false
		}
		err := exec.Command("security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", value).Run()
		return err == nil
	case "linux":
		if !hasCommand("secret-tool") {
			return false
		}
		cmd := exec.Command("secret-tool", "store", "--label", service+":"+account, "service", service, "account", account)
		cmd.Stdin = strings.NewReader(value)
		return cmd.Run() == nil
	default:
		return false
	}
}

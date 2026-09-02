//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsDetachedProcess       = 0x00000008
	windowsCreateBreakaway       = 0x01000000
)

func removeNativeSelfBinary(exe string) (binaryRemovalResult, error) {
	result := binaryRemovalResult{Removed: []string{}, Scheduled: []string{}}
	if !isWindowsNativeBinary(exe) {
		result.Hint = "当前可执行文件不是 yoooclaw.exe 或 yc.exe，请手动删除二进制"
		return result, nil
	}

	dir := filepath.Dir(exe)
	candidates := []string{
		exe,
		filepath.Join(dir, "yoooclaw.exe"),
		filepath.Join(dir, "yc.exe"),
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		key := strings.ToLower(cleaned)
		if seen[key] || !isWindowsNativeBinary(cleaned) {
			continue
		}
		seen[key] = true
		if _, err := os.Lstat(cleaned); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, err
		}
		result.Scheduled = append(result.Scheduled, cleaned)
	}
	if len(result.Scheduled) == 0 {
		return result, nil
	}

	encoded := encodePowerShellCommand(windowsRemovalScript(os.Getpid(), result.Scheduled))
	if err := startWindowsRemovalHelper(encoded, true); err != nil {
		// 某些 Agent 会把 CLI 放进不允许 breakaway 的 Job Object；这种情况
		// 回退到普通 detached 子进程，至少覆盖常规终端和安装器环境。
		if fallbackErr := startWindowsRemovalHelper(encoded, false); fallbackErr != nil {
			return result, fmt.Errorf("启动退出后删除任务失败：%v；回退重试失败：%w", err, fallbackErr)
		}
	}
	result.Hint = "Windows 已安排在当前命令退出后删除 CLI 二进制"
	return result, nil
}

func isWindowsNativeBinary(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "yoooclaw.exe" || base == "yc.exe"
}

func startWindowsRemovalHelper(encodedCommand string, breakaway bool) error {
	flags := uint32(windowsCreateNewProcessGroup | windowsDetachedProcess)
	if breakaway {
		flags |= windowsCreateBreakaway
	}
	cmd := exec.Command(
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-WindowStyle", "Hidden",
		"-EncodedCommand", encodedCommand,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: flags,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

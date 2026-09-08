//go:build windows

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/installer"
	cliVersion "github.com/YoooClaw/cli/internal/version"
)

func applyNativeUpdate(ctx *clictx.Context, version string) (any, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if !isWindowsNativeBinary(exe) || cliVersion.Dist() != "native" {
		return nil, fmt.Errorf("请先使用原生 setup 安装，当前入口不是 yoooclaw.exe/yc.exe")
	}
	dir, err := os.MkdirTemp("", "yoooclaw-update-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(dir) // empty only; leave evidence if cleanup fails
	downloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	setup, err := installer.DownloadSetup(downloadCtx, http.DefaultClient, installer.ReleaseBase, version, dir)
	if err != nil {
		return nil, err
	}
	defer os.Remove(setup)
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer verifyCancel()
	verify := exec.CommandContext(verifyCtx, setup, "--version")
	verify.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsCreateNoWindow}
	actual, verifyErr := verify.Output()
	if verifyErr != nil || strings.TrimSpace(string(actual)) != version {
		return nil, fmt.Errorf("下载的 setup 版本与请求版本不符或无法执行；未修改现有安装")
	}
	// Do not impose a timer that can kill setup halfway through its transaction.
	command := exec.Command(setup, "--yes", "--force", "--install-dir", filepath.Dir(exe), "--profile", ctx.Profile, "--format", "json")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsCreateNoWindow}
	out, err := command.Output()
	if err != nil {
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(out, &failure) == nil && failure.Error.Message != "" {
			return nil, fmt.Errorf("原生升级安装器失败: %s", failure.Error.Message)
		}
		return nil, fmt.Errorf("原生升级安装器失败: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("无法解析安装器验收结果: %w", err)
	}
	if result["ok"] != true || result["installed"] != true || result["version"] != version {
		return nil, fmt.Errorf("安装器未确认目标版本安装成功")
	}
	result["applied"] = true
	return result, nil
}

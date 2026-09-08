//go:build windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/autostart"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/installer"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/YoooClaw/cli/internal/version"
	"github.com/YoooClaw/cli/internal/winhost"
	"github.com/YoooClaw/cli/internal/winproc"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

func installNative(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if flagBool(cmd, "activate") && ctx.Profile != persistentActiveProfile() {
		return nil, errs.New(errs.CodeInvalidArgument, "安装时 --activate 只支持当前 active profile；先明确切换 profile 后重试")
	}
	if !flagBool(cmd, "yes") {
		ok, err := prompt.Confirm("安装 YoooClaw 到当前用户目录并配置 PATH？已有配置及数据会保留", false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.New(errs.CodeConfirmationRequired, "已取消安装")
		}
	}
	dir := flagStr(cmd, "install-dir")
	if dir == "" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return nil, fmt.Errorf("LOCALAPPDATA 未设置，无法确定当前用户安装目录")
		}
		dir = filepath.Join(base, "YoooClaw", "bin")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	source, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if len(installer.BundledCLI) > 0 {
		payloadDir, err := os.MkdirTemp("", "yoooclaw-setup-payload-")
		if err != nil {
			return nil, err
		}
		defer os.Remove(payloadDir)
		source = filepath.Join(payloadDir, "yoooclaw.exe")
		defer os.Remove(source)
		if err := os.WriteFile(source, installer.BundledCLI, 0o700); err != nil {
			return nil, err
		}
	}
	if _, err := winhost.Bytes(); err != nil {
		return nil, err
	}
	// Serialize install/update in this user session without stale lock files.
	// Windows mutex ownership is thread-affine, not goroutine-affine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mutexName, _ := windows.UTF16PtrFromString(`Local\YoooClawNativeInstall`)
	mutex, err := windows.CreateMutex(nil, true, mutexName)
	if err != nil {
		if mutex != 0 {
			windows.CloseHandle(mutex)
		}
		return nil, fmt.Errorf("另一个安装可能正在运行: %w", err)
	}
	defer windows.CloseHandle(mutex)
	defer windows.ReleaseMutex(mutex)
	pathBefore, err := readWindowsUserPath()
	if err != nil {
		return nil, err
	}
	manager := autostartManager()
	service, statusErr := manager.Status()
	// Unknown service state must never lead to stopping a healthy daemon.
	if statusErr != nil {
		return nil, autostartError(statusErr)
	}
	active := persistentActiveProfile()
	previous := map[string]*daemon.Lock{}
	for _, profile := range paths.ListProfileNames() {
		state := daemon.State(paths.For(profile))
		if state.Running && state.Lock != nil {
			if state.Lock.Owner != "" {
				continue
			} // Do not disrupt third-party owners.
			identity, err := winproc.Inspect(uint32(state.Lock.PID))
			if err != nil {
				return nil, fmt.Errorf("无法验证 profile %s 旧进程身份，不停止它: %w", profile, err)
			}
			if state.Lock.Executable == "" || !strings.EqualFold(filepath.Clean(state.Lock.Executable), filepath.Clean(identity.Executable)) {
				return nil, fmt.Errorf("profile %s daemon 锁缺少可信程序路径或 PID 身份不匹配，不停止它", profile)
			}
			copy := *state.Lock
			previous[profile] = &copy
		}
	}
	pathChanged := false
	var cleanupPending []string
	staleWarnings := cleanupStaleWindowsUninstallFiles(filepath.Join(dir, "yoooclaw.exe"))
	restore := func() error {
		var failures []error
		if service.Running {
			if err := manager.Start(); err != nil {
				failures = append(failures, err)
			}
		}
		for profile, lock := range previous {
			// The task owns the active profile. Do not race its asynchronous
			// start with a second child, but still restore other profiles.
			if service.Running && profile == active {
				continue
			}
			if daemon.State(paths.For(profile)).Running {
				continue
			}
			exe := lock.Executable
			if exe == "" {
				exe = filepath.Join(dir, "yoooclaw.exe")
			}
			if err := startInstalledForeground(exe, profile, lock); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	}
	transaction, err := installer.Install(source, dir, flagBool(cmd, "force"), installer.Hooks{
		Stop: func() error {
			if service.Running {
				if err := manager.Stop(); err != nil {
					return err
				}
			}
			for profile := range previous {
				if _, err := daemon.Stop(paths.For(profile)); err != nil {
					return err
				}
			}
			return nil
		},
		Verify: func(target string) error {
			commandCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			check := exec.CommandContext(commandCtx, target, "--version")
			check.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsCreateNoWindow}
			out, err := check.Output()
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(out)) != version.Version {
				return fmt.Errorf("安装后版本校验失败: %s", target)
			}
			return nil
		},
		CommitPath: func() error {
			if flagBool(cmd, "no-modify-path") {
				return nil
			}
			without, _ := removeWindowsPathEntry(pathBefore.Value, dir)
			value := dir
			if without != "" {
				value += ";" + without
			}
			pathChanged = true
			return writeWindowsUserPath(windowsUserPathState{Exists: true, Value: value, ValueType: pathBefore.ValueType})
		},
		RestorePath: func() error {
			if pathChanged {
				return writeWindowsUserPath(pathBefore)
			}
			return nil
		},
		Resume: restore,
		RemoveBackup: func(path string) error {
			if err := os.Remove(path); err == nil {
				return nil
			}
			if err := removeWindowsPathNow(path); err == nil {
				return nil
			}
			root := windowsUninstallTempRoot(filepath.Join(dir, "yoooclaw.exe"))
			if err := os.MkdirAll(root, 0o700); err != nil {
				return err
			}
			file, err := os.CreateTemp(root, "yoooclaw-upgrade-*.exe.pending")
			if err != nil {
				return err
			}
			pending := file.Name()
			if err := file.Close(); err != nil {
				return err
			}
			if err := os.Remove(pending); err != nil {
				return err
			}
			if err := os.Rename(path, pending); err != nil {
				return err
			}
			if err := startWindowsRemovalHelper(pending); err != nil {
				_ = os.Rename(pending, path)
				return err
			}
			cleanupPending = append(cleanupPending, pending)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "installed": true, "version": version.Version, "executable": filepath.Join(dir, "yoooclaw.exe"), "aliases": transaction.Installed, "dataKept": true, "configurationKept": true, "userPathConfigured": !flagBool(cmd, "no-modify-path"), "daemonRestored": false}
	if len(cleanupPending) > 0 {
		result["cleanupPending"] = cleanupPending
	}
	warnings := append(transaction.Warnings, staleWarnings...)
	if pathChanged {
		if err := broadcastWindowsEnvironmentChange(); err != nil {
			warnings = append(warnings, "PATH 已写入；请重新打开终端: "+err.Error())
		}
	}
	// Never invoke npm.cmd/.ps1. Keeping a third-party npm installation is safer
	// than manually deleting its package/shims. Always hand back the native path.
	if found, err := exec.LookPath("yoooclaw"); err == nil && !strings.EqualFold(found, filepath.Join(dir, "yoooclaw.exe")) {
		warnings = append(warnings, "当前会话仍可能命中旧入口；请使用返回的 executable 绝对路径，重开终端后核对 PATH。旧 npm 包未删除。")
	}
	if flagBool(cmd, "activate") {
		_, err = activateCLIOwner(ctx, flagStr(cmd, "hermes-profile"), false)
		if err != nil {
			warnings = append(warnings, "安装成功，owner 交接未完成: "+err.Error())
		}
	}
	activeCtx, _ := clictx.Build(active, "json", true, false)
	desired, desiredErr := autostart.Desired(paths.RootDir())
	if desiredErr != nil {
		warnings = append(warnings, desiredErr.Error())
	}
	if config.Exists(activeCtx.Paths) && desiredErr == nil && err == nil {
		precheck := daemon.PrecheckStart(activeCtx, daemon.StartOpts{})
		if precheck == nil && desired != autostart.DesiredDisabled {
			spec, _ := autostart.ResolveSpec(paths.RootDir())
			spec.Executable = filepath.Join(dir, "yoooclaw.exe")
			start := !flagBool(cmd, "no-start") && (previous[active] != nil || flagBool(cmd, "activate") || service.Running)
			_, enableErr := autostart.Enable(manager, spec, start)
			if enableErr == nil && start {
				enableErr = waitForManagedDaemon(activeCtx)
			}
			if enableErr != nil {
				warnings = append(warnings, "安装成功，系统托管未就绪: "+enableErr.Error())
				result["autostartConfigured"] = false
			} else {
				result["autostartConfigured"] = true
				result["daemonRestored"] = start
			}
		} else if precheck != nil {
			warnings = append(warnings, "保留当前 owner，未启动独立 daemon: "+precheck.Error())
		} else if !flagBool(cmd, "no-start") && previous[active] != nil {
			if err := startInstalledForeground(filepath.Join(dir, "yoooclaw.exe"), active, previous[active]); err != nil {
				warnings = append(warnings, err.Error())
			} else {
				result["daemonRestored"] = true
			}
		}
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	if !flagBool(cmd, "no-start") && !flagBool(cmd, "activate") {
		for profile, lock := range previous {
			if profile == active {
				continue
			}
			if err := startInstalledForeground(filepath.Join(dir, "yoooclaw.exe"), profile, lock); err != nil {
				warnings = append(warnings, err.Error())
			}
		}
		if len(warnings) > 0 {
			result["warnings"] = warnings
		}
	}
	result["hint"] = "安装已验证；当前终端 PATH 不会被子进程更新，请使用 executable 绝对路径或重开终端。连接需另外查询 daemon/tunnel 状态。"
	return result, nil
}

func startInstalledForeground(exe, profile string, lock *daemon.Lock) error {
	args := []string{"--profile", profile, "daemon", "run-foreground"}
	if lock.Bind != "" {
		args = append(args, "--bind", lock.Bind)
	}
	if lock.Port != 0 {
		args = append(args, "--port", fmt.Sprint(lock.Port))
	}
	if lock.LogLevel != "" {
		args = append(args, "--log-level", lock.LogLevel)
	}
	command := exec.Command(exe, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsCreateNoWindow | windowsCreateNewProcessGroup}
	if err := command.Start(); err != nil {
		return err
	}
	defer command.Process.Release()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state := daemon.State(paths.For(profile))
		if state.Running && state.Lock != nil && state.Lock.PID == command.Process.Pid {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = command.Process.Kill()
	return fmt.Errorf("恢复 profile %s daemon 超时", profile)
}

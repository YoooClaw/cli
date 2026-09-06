package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/autostart"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/spf13/cobra"
)

const managedDaemonReadyTimeout = 15 * time.Second

func newDaemonAutostartCmd() *cobra.Command {
	c := &cobra.Command{Use: "autostart", Short: "管理用户登录时自动启动"}
	enable := &cobra.Command{Use: "enable", Short: "启用自启并立即启动 daemon", Args: cobra.NoArgs, RunE: run(daemonAutostartEnable)}
	enable.Flags().Bool("no-start", false, "只启用自启，当前不启动")
	disable := &cobra.Command{Use: "disable", Short: "停止 daemon 并关闭自启", Args: cobra.NoArgs, RunE: run(daemonAutostartDisable)}
	status := &cobra.Command{Use: "status", Short: "显示自启期望与系统服务状态", Args: cobra.NoArgs, RunE: run(daemonAutostartStatus)}
	migrate := &cobra.Command{Use: "migrate", Short: "迁移旧版本的 daemon 自启状态", Hidden: true, Args: cobra.NoArgs, RunE: run(daemonAutostartMigrate)}
	c.AddCommand(enable, disable, status, migrate)
	return c
}

func autostartError(err error) error {
	if err == nil {
		return nil
	}
	return errs.New(errs.CodeAutostartUnavailable, "无法配置 daemon 自启："+err.Error()).
		WithHint("检查用户级服务管理器后重试；也可用 `yoooclaw daemon start` 手动运行")
}

func autostartManager() autostart.Manager { return autostart.Current(paths.RootDir()) }

func autostartSpec() (autostart.Spec, error) { return autostart.ResolveSpec(paths.RootDir()) }

func autostartSnapshot() (map[string]any, error) {
	root := paths.RootDir()
	desired, stateErr := autostart.Desired(root)
	manager := autostartManager()
	status, statusErr := manager.Status()
	if statusErr != nil {
		return nil, statusErr
	}
	state, exists, _ := autostart.ReadState(root)
	drift := desired == autostart.DesiredEnabled && !status.Installed
	if desired == autostart.DesiredDisabled && status.Installed {
		drift = true
	}
	if exists && state.Executable != "" && status.Executable != "" && state.Executable != status.Executable {
		drift = true
	}
	if currentSpec, specErr := autostartSpec(); specErr == nil && exists && state.Executable != "" && state.Executable != currentSpec.Executable {
		drift = true
	}
	result := map[string]any{
		"ok": true, "desired": desired, "configured": exists,
		"manager": status.Manager, "unit": status.Unit,
		"installed": status.Installed, "loaded": status.Loaded,
		"running": status.Running, "drift": drift,
		"profile": persistentActiveProfile(),
	}
	if desired == "" {
		result["desired"] = "unknown"
	}
	if status.PID > 0 {
		result["pid"] = status.PID
	}
	if exists && state.Executable != "" {
		result["executable"] = state.Executable
		_, executableErr := os.Stat(state.Executable)
		result["executableExists"] = executableErr == nil
		if os.IsNotExist(executableErr) {
			result["drift"] = true
		}
	}
	if stateErr != nil {
		result["stateError"] = stateErr.Error()
	}
	return result, nil
}

func persistentActiveProfile() string {
	if active := paths.ReadActiveProfile(); active != "" {
		return active
	}
	return paths.DefaultProfile
}

func daemonAutostartStatus(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	result, err := autostartSnapshot()
	if err != nil {
		return nil, autostartError(err)
	}
	return result, nil
}

// daemonAutostartMigrate is called by installers after an upgrade. It only
// supplies the new default for installations that predate autostart.json;
// an explicit user opt-out is never overwritten. Registration is deliberately
// cold: a daemon that was stopped before the upgrade remains stopped until the
// next login, while an installer can separately restore one that was running.
func daemonAutostartMigrate(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	root := paths.RootDir()
	desired, err := autostart.Desired(root)
	if err != nil {
		return nil, err
	}
	if desired == autostart.DesiredDisabled {
		return map[string]any{"ok": true, "migrated": false, "desired": desired, "reason": "用户已明确关闭自启"}, nil
	}
	if ctx.Profile != persistentActiveProfile() {
		return map[string]any{"ok": true, "migrated": false, "desired": desiredOrUnknown(desired), "reason": "只迁移 active profile"}, nil
	}
	if !config.Exists(ctx.Paths) {
		return map[string]any{"ok": true, "migrated": false, "desired": desiredOrUnknown(desired), "reason": "active profile 尚未初始化"}, nil
	}
	// A running daemon already proves that the active standalone owner is
	// valid. Probing the account Relay lock in that state would mistake the
	// daemon's own lock for a conflicting profile.
	if state := daemon.State(ctx.Paths); !state.Running {
		if err := daemon.PrecheckStart(ctx, daemon.StartOpts{}); err != nil {
			var structured *errs.Error
			if errors.As(err, &structured) && structured.Code == errs.CodeDaemonDisabledByPlugin {
				return map[string]any{"ok": true, "migrated": false, "desired": desiredOrUnknown(desired), "reason": structured.Message}, nil
			}
			return nil, err
		}
	}
	spec, err := autostartSpec()
	if err != nil {
		return nil, autostartError(err)
	}
	status, err := autostart.Enable(autostartManager(), spec, false)
	if err != nil {
		return nil, autostartError(err)
	}
	result, snapshotErr := autostartSnapshot()
	if snapshotErr != nil {
		return nil, autostartError(snapshotErr)
	}
	result["migrated"] = desired == ""
	result["repaired"] = desired == autostart.DesiredEnabled
	result["manager"] = status.Manager
	return result, nil
}

func desiredOrUnknown(desired string) string {
	if desired == "" {
		return "unknown"
	}
	return desired
}

func daemonAutostartEnable(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if ctx.Profile != persistentActiveProfile() {
		return nil, errs.New(errs.CodeInvalidArgument, "自启服务只运行 active profile `"+persistentActiveProfile()+"`").
			WithHint("先运行 `yoooclaw profile use " + ctx.Profile + "`")
	}
	if _, err := config.Require(ctx.Paths); err != nil {
		return nil, err
	}
	start := !flagBool(cmd, "no-start")
	manager := autostartManager()
	// Do every non-mutating service-manager check before stopping a healthy
	// daemon. Managed Agent environments can block service operations; finding
	// that out only after Stop leaves the client offline with no persistent
	// replacement. Windows performs this probe through Task Scheduler COM.
	if err := manager.Available(); err != nil {
		return nil, recoverDaemonAfterAutostartFailure(ctx, start, err)
	}
	status, err := manager.Status()
	if err != nil {
		return nil, recoverDaemonAfterAutostartFailure(ctx, start, err)
	}
	spec, err := autostartSpec()
	if err != nil {
		return nil, recoverDaemonAfterAutostartFailure(ctx, start, err)
	}
	if status.Running {
		if err := manager.Stop(); err != nil {
			return nil, recoverDaemonAfterAutostartFailure(ctx, start, err)
		}
	}
	if state := daemon.State(ctx.Paths); state.Running {
		if _, err := daemon.Stop(ctx.Paths); err != nil {
			return nil, err
		}
	}
	if err := daemon.PrecheckStart(ctx, daemon.StartOpts{}); err != nil {
		return nil, err
	}
	status, err = autostart.Enable(manager, spec, start)
	if err != nil {
		return nil, recoverDaemonAfterAutostartFailure(ctx, start, err)
	}
	if start && status.Manager != "test" {
		if err := waitForManagedDaemon(ctx); err != nil {
			return nil, recoverDaemonAfterAutostartFailure(ctx, true, err)
		}
	}
	result, _ := autostartSnapshot()
	return result, nil
}

func recoverDaemonAfterAutostartFailure(ctx *clictx.Context, start bool, cause error) error {
	if !start || daemon.State(ctx.Paths).Running {
		return autostartError(cause)
	}
	if _, err := daemon.Spawn(ctx, daemon.StartOpts{}); err != nil {
		return errs.New(errs.CodeAutostartUnavailable,
			"无法配置 daemon 自启："+cause.Error()+"；恢复 standalone daemon 也失败："+err.Error()).
			WithHint("检查 `yoooclaw daemon logs` 后运行 `yoooclaw daemon start`")
	}
	return errs.New(errs.CodeAutostartUnavailable,
		"无法配置 daemon 自启："+cause.Error()+"；已恢复当前 standalone daemon，但尚未配置登录自启").
		WithHint("当前连接可继续使用；退出受限 Agent 后重试 `yoooclaw daemon autostart enable`")
}

func daemonAutostartDisable(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	manager := autostartManager()
	if _, err := autostart.Disable(manager, paths.RootDir()); err != nil {
		return nil, autostartError(err)
	}
	// Also retire a legacy detached daemon that is not owned by the manager.
	for _, name := range paths.ListProfileNames() {
		p := paths.For(name)
		if state := daemon.State(p); state.Running {
			if _, err := daemon.Stop(p); err != nil {
				return nil, err
			}
		}
	}
	result, _ := autostartSnapshot()
	return result, nil
}

func serviceManaged() (bool, autostart.Status) {
	desired, _ := autostart.Desired(paths.RootDir())
	status, _ := autostartManager().Status()
	return desired == autostart.DesiredEnabled || status.Installed, status
}

func startStandalone(ctx *clictx.Context) (*daemon.Lock, error) {
	managed, status := serviceManaged()
	if !managed {
		return daemon.Spawn(ctx, daemon.StartOpts{})
	}
	if status.Running {
		state := daemon.State(ctx.Paths)
		if state.Running {
			return state.Lock, nil
		}
	}
	manager := autostartManager()
	spec, err := autostartSpec()
	if err != nil {
		return nil, autostartError(err)
	}
	// Reinstall is idempotent and refreshes an executable path that may have
	// changed during an npm/native update.
	if _, err := autostart.Enable(manager, spec, false); err != nil {
		return nil, autostartError(err)
	}
	if err := manager.Start(); err != nil {
		return nil, autostartError(err)
	}
	if status.Manager == "test" {
		return &daemon.Lock{}, nil
	}
	if err := waitForManagedDaemon(ctx); err != nil {
		return nil, err
	}
	return daemon.State(ctx.Paths).Lock, nil
}

func waitForManagedDaemon(ctx *clictx.Context) error {
	deadline := time.Now().Add(managedDaemonReadyTimeout)
	for time.Now().Before(deadline) {
		state := daemon.State(ctx.Paths)
		if state.Running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errs.New(errs.CodeDaemonNotRunning, "系统服务已启动，但 daemon 未在 "+managedDaemonReadyTimeout.String()+" 内就绪").
		WithHint("查看 `yoooclaw daemon logs` 和 `yoooclaw daemon logs --supervisor`")
}

func stopManagedDaemon(ctx *clictx.Context, opts daemon.StopOpts) (any, error) {
	// Lifecycle-filtered stops target a specific hosted process and must retain
	// the daemon package's owner/generation checks.
	if opts.Owner != "" || opts.Generation != "" {
		return daemon.StopWithOptions(ctx.Paths, opts)
	}
	managed, status := serviceManaged()
	if managed && status.Loaded {
		pid := 0
		if state := daemon.State(ctx.Paths); state.Running {
			pid = state.Lock.PID
		}
		if err := autostartManager().Stop(); err != nil {
			return nil, autostartError(err)
		}
		waitForProfileDaemonExit(pid)
		if state := daemon.State(ctx.Paths); state.Running {
			if _, err := daemon.StopWithOptions(ctx.Paths, opts); err != nil {
				return nil, err
			}
		}
		return map[string]any{"ok": true, "stopped": true, "supervised": true, "autostart": true}, nil
	}
	return daemon.StopWithOptions(ctx.Paths, opts)
}

func restartManagedDaemon(ctx *clictx.Context) (any, error) {
	managed, status := serviceManaged()
	if !managed {
		lock, err := daemon.Spawn(ctx, daemon.StartOpts{})
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "pid": lock.PID, "bind": lock.Bind, "port": lock.Port, "detached": true}, nil
	}
	manager := autostartManager()
	spec, err := autostartSpec()
	if err != nil {
		return nil, autostartError(err)
	}
	if _, err := autostart.Enable(manager, spec, false); err != nil {
		return nil, autostartError(err)
	}
	if err := manager.Restart(); err != nil {
		return nil, autostartError(err)
	}
	if status.Manager != "test" {
		if err := waitForManagedDaemon(ctx); err != nil {
			return nil, err
		}
	}
	state := daemon.State(ctx.Paths)
	result := map[string]any{"ok": true, "supervised": true, "autostart": true}
	if state.Lock != nil {
		result["pid"] = state.Lock.PID
		result["port"] = state.Lock.Port
	}
	return result, nil
}

func daemonRunServiceWithRoot(ctx *clictx.Context, cmd *cobra.Command) (*clictx.Context, error) {
	root := flagStr(cmd, "root")
	if root == "" {
		return ctx, nil
	}
	if err := os.Setenv("YOOOCLAW_HOME", root); err != nil {
		return nil, err
	}
	profile := persistentActiveProfile()
	return &clictx.Context{
		Profile: profile, Paths: paths.For(profile), Format: ctx.Format,
		Quiet: ctx.Quiet, Color: ctx.Color,
	}, nil
}

func daemonRunService(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	ctx, err := daemonRunServiceWithRoot(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if !config.Exists(ctx.Paths) {
		return map[string]any{"ok": true, "running": false, "skipped": "active profile 尚未初始化", "profile": ctx.Profile}, nil
	}
	err = daemon.RunForeground(ctx, daemon.StartOpts{})
	if err == nil {
		return map[string]any{"ok": true, "stopped": true}, nil
	}
	var structured *errs.Error
	if errors.As(err, &structured) && (structured.Code == errs.CodeDaemonDisabledByPlugin || structured.Code == errs.CodeDaemonAlreadyRunning) {
		appendSupervisorLog("INFO", structured.Message)
		return map[string]any{"ok": true, "running": false, "skipped": structured.Message, "profile": ctx.Profile}, nil
	}
	appendSupervisorLog("ERROR", err.Error())
	return nil, err
}

func appendSupervisorLog(level, message string) {
	file := filepath.Join(paths.RootDir(), "logs", "daemon-supervisor.log")
	if err := fsutil.EnsureDir(filepath.Dir(file), fsutil.DirMode); err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, fsutil.SecretFileMode)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339), level, message)
}

func managedOverrides(cmd *cobra.Command) bool {
	return flagStr(cmd, "bind") != "" || flagStr(cmd, "port") != "" || flagStr(cmd, "log-level") != "" ||
		flagStr(cmd, "owner") != "" || flagStr(cmd, "generation") != "" || flagStr(cmd, "ingress") != "" ||
		flagStr(cmd, "egress-callback-url") != "" || flagStr(cmd, "egress-callback-token") != "" || flagBool(cmd, "no-detach")
}

func rejectManagedOverrides(cmd *cobra.Command) error {
	managed, _ := serviceManaged()
	if managed && managedOverrides(cmd) {
		return errs.New(errs.CodeInvalidArgument, "自启托管模式不接受临时 daemon 启动参数").
			WithHint("用 `yoooclaw config set daemon.<key> <value>` 持久化配置，或先关闭 autostart")
	}
	return nil
}

func removeAutostartForUninstall() ([]string, error) {
	manager := autostartManager()
	status, statusErr := manager.Status()
	if statusErr != nil {
		return nil, autostartError(statusErr)
	}
	if status.Installed || status.Loaded {
		if err := manager.Uninstall(); err != nil {
			return nil, autostartError(err)
		}
	}
	statePath := autostart.StatePath(paths.RootDir())
	if err := os.Remove(statePath); err == nil {
		return []string{statePath}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return nil, nil
}

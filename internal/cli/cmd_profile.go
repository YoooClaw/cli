package cli

import (
	"os"
	"sort"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

func newProfileCmd() *cobra.Command {
	c := &cobra.Command{Use: "profile", Short: "多 profile 管理 🟢"}

	listCmd := &cobra.Command{Use: "list", Short: "列出所有 profile，标注 active", Args: cobra.NoArgs, RunE: run(profileList)}
	useCmd := &cobra.Command{Use: "use <name>", Short: "切换 active profile", Args: cobra.ExactArgs(1), RunE: run(profileUse)}
	createCmd := &cobra.Command{Use: "create <name>", Short: "新建 profile（走 config init 向导）", Args: cobra.ExactArgs(1), RunE: run(profileCreate)}
	createCmd.Flags().Bool("non-interactive", false, "跳过向导（配合 --from-file）")
	createCmd.Flags().String("from-file", "", "从 JSON 文件导入配置（- 为 stdin）")
	createCmd.Flags().Bool("force", false, "已存在 config 时覆盖")
	createCmd.Flags().Bool("no-start", false, "当前不启动 daemon")
	createCmd.Flags().Bool("no-autostart", false, "不配置用户登录自启")
	deleteCmd := &cobra.Command{Use: "delete <name>", Short: "删除 profile（非 active，需 --yes）", Args: cobra.ExactArgs(1), RunE: run(profileDelete)}
	deleteCmd.Flags().Bool("yes", false, "跳过确认")

	c.AddCommand(listCmd, useCmd, createCmd, deleteCmd)
	return c
}

func profileList(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	active := paths.ReadActiveProfile()
	if active == "" {
		active = paths.DefaultProfile
	}
	names := paths.ListProfileNames()
	for _, implied := range []string{active, paths.DefaultProfile} {
		if !contains(names, implied) {
			names = append(names, implied)
		}
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{
			"name":        name,
			"active":      name == active,
			"initialized": config.Exists(paths.For(name)),
		})
	}
	return out, nil
}

func profileUse(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	name := args[0]
	p := paths.For(name)
	if !fsutil.Exists(p.Dir) {
		return nil, errs.New(errs.CodeProfileNotFound, "profile `"+name+"` 不存在",
			map[string]any{"hint": "用 yoooclaw profile create 新建", "checkedPaths": []string{p.Dir}})
	}

	previous := paths.ReadActiveProfile()
	if previous == "" {
		previous = paths.DefaultProfile
	}
	if previous == name {
		// effective profile 可能来自“失效 active-profile → default”回退；仍然重写
		// 文件，修复手工删除 profile 后遗留的旧名称。
		if err := fsutil.WriteAtomic(paths.ActiveProfilePath(), []byte(name+"\n"), fsutil.ConfigFileMode); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "active": name, "previous": previous, "changed": false}, nil
	}

	// active profile 与 daemon 的存储根目录必须一起切换。旧行为只改文本文件，
	// 旧 daemon 会继续用 test profile 消费生产 Relay，造成“消息仍落测试区”。
	managed, serviceStatus := serviceManaged()
	serviceWasRunning := managed && serviceStatus.Running
	oldPID := 0
	if oldState := daemon.State(paths.For(previous)); oldState.Running {
		oldPID = oldState.Lock.PID
	}
	if serviceWasRunning {
		if err := autostartManager().Stop(); err != nil {
			return nil, autostartError(err)
		}
		waitForProfileDaemonExit(oldPID)
	}
	stoppedPID, err := stopProfileDaemon(paths.For(previous))
	if err != nil {
		return nil, err
	}
	if err := fsutil.WriteAtomic(paths.ActiveProfilePath(), []byte(name+"\n"), fsutil.ConfigFileMode); err != nil {
		return nil, err
	}

	result := map[string]any{
		"ok": true, "active": name, "previous": previous, "changed": true,
	}
	if stoppedPID == 0 && !serviceWasRunning {
		return result, nil
	}
	// daemon 会在退出末尾先删 profile lock、随后由进程退出释放 account Relay
	// flock；两者之间存在极短窗口。等旧 PID 真正消失，避免目标启动误报全局锁冲突。
	if stoppedPID != 0 {
		waitForProfileDaemonExit(stoppedPID)
	}

	// 旧 profile 原本有 daemon 在提供服务时，切换后尽力恢复同等运行态。目标
	// profile 未初始化或启动失败不回滚到旧 profile：宁可明确停住，也不能让旧
	// profile 再次静默消费新环境消息。
	if stoppedPID == 0 {
		stoppedPID = oldPID
	}
	daemonInfo := map[string]any{"stopped": stoppedPID, "started": false, "supervised": serviceWasRunning}
	result["daemon"] = daemonInfo
	if targetState := daemon.State(p); targetState.Running {
		daemonInfo["started"] = true
		daemonInfo["alreadyRunning"] = true
		daemonInfo["pid"] = targetState.Lock.PID
		daemonInfo["port"] = targetState.Lock.Port
		return result, nil
	}
	if !config.Exists(p) {
		daemonInfo["reason"] = "目标 profile 尚未初始化"
		daemonInfo["hint"] = "运行 `yoooclaw config init` 后再启动 daemon"
		return result, nil
	}
	targetCtx := &clictx.Context{
		Profile: name, Paths: p, Format: ctx.Format, Quiet: ctx.Quiet, Color: ctx.Color,
	}
	if err := daemon.PrecheckStart(targetCtx, daemon.StartOpts{}); err != nil {
		daemonInfo["error"] = err.Error()
		daemonInfo["hint"] = "修复启动条件后运行 `yoooclaw daemon start`"
		return result, nil
	}
	lock, err := startStandalone(targetCtx)
	if err != nil {
		daemonInfo["error"] = err.Error()
		daemonInfo["hint"] = "查看 `yoooclaw daemon logs` 后重新启动"
		return result, nil
	}
	daemonInfo["started"] = true
	if lock != nil && lock.PID > 0 {
		daemonInfo["pid"] = lock.PID
		daemonInfo["port"] = lock.Port
	}
	return result, nil
}

func profileCreate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	name := args[0]
	p := paths.For(name)
	if config.Exists(p) {
		return nil, errs.New(errs.CodeAlreadyExists, "profile `"+name+"` 已存在")
	}
	subCtx := &clictx.Context{Profile: name, Paths: p, Format: ctx.Format, Quiet: ctx.Quiet, Color: ctx.Color}
	return initCore(subCtx, initOpts{
		force:          flagBool(cmd, "force"),
		nonInteractive: flagBool(cmd, "non-interactive"),
		fromFile:       flagStr(cmd, "from-file"),
		noStart:        flagBool(cmd, "no-start"),
		noAutostart:    flagBool(cmd, "no-autostart"),
	})
}

func profileDelete(_ *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	name := args[0]
	active := paths.ReadActiveProfile()
	if active == "" {
		active = paths.DefaultProfile
	}
	if name == active {
		return nil, errs.New(errs.CodeInvalidArgument, "不能删除当前 active profile `"+name+"`",
			map[string]any{"hint": "先 yoooclaw profile use <其他 profile> 再删除"})
	}
	p := paths.For(name)
	if !fsutil.Exists(p.Dir) {
		return nil, errs.New(errs.CodeProfileNotFound, "profile `"+name+"` 不存在")
	}
	if !flagBool(cmd, "yes") {
		ok, err := prompt.Confirm("确认删除 profile `"+name+"` 及其全部数据？", false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.New(errs.CodeConfirmationRequired, "已取消")
		}
	}
	stoppedPID, err := stopProfileDaemon(p)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "deleted": name}
	if stoppedPID != 0 {
		result["daemonStopped"] = stoppedPID
	}
	return result, nil
}

// stopProfileDaemon 在切换或删除 profile 前停止对应进程。锁刚好在检查后变陈旧
// 时按“已经停止”处理，避免无害竞态阻断 profile 操作。
func stopProfileDaemon(p paths.Paths) (int, error) {
	state := daemon.State(p)
	if !state.Running {
		if state.Stale {
			daemon.RemoveLock(p)
		}
		return 0, nil
	}
	pid := state.Lock.PID
	if _, err := daemon.Stop(p); err != nil {
		if latest := daemon.State(p); !latest.Running {
			daemon.RemoveLock(p)
			return pid, nil
		}
		return 0, err
	}
	return pid, nil
}

func waitForProfileDaemonExit(pid int) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(time.Second)
	for daemon.IsProcessAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

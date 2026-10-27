package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/skills"
	updater "github.com/YoooClaw/cli/internal/update"
	"github.com/spf13/cobra"
)

func addRolloutCommands(c *cobra.Command) {
	check := &cobra.Command{Use: "check", Short: "检查更新并安排至少 10 分钟后空闲安装", Args: cobra.NoArgs, RunE: run(updateCheck)}
	check.Flags().Bool("check-only", false, "只查询，不安排后台安装")
	check.Flags().String("version", "", "复查指定版本（不绕过灰度）")
	install := &cobra.Command{Use: "install", Short: "后台安装灰度平台允许的版本", Args: cobra.NoArgs, RunE: run(updateInstall)}
	install.Flags().String("version", "", "安装指定的有资格版本")
	register := &cobra.Command{Use: "_register", Hidden: true, Args: cobra.NoArgs, RunE: run(func(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
		exe, err := os.Executable()
		if err == nil {
			err = updater.Register(paths.RootDir(), exe)
		}
		return map[string]any{"ok": err == nil}, err
	})}
	apply := &cobra.Command{Use: "_apply <context-file>", Hidden: true, Args: cobra.ExactArgs(1), RunE: run(updateApply)}
	begin := &cobra.Command{Use: "_report-begin <version>", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := updater.NewIdentity(paths.For(clictx.ResolveActiveProfile(flagStr(cmd, "profile"))))
		if err != nil {
			return err
		}
		a, err := updater.BeginOfficial(id, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), updater.JobPath(a))
		return nil
	}}
	report := &cobra.Command{Use: "_report <context> <stage> <result>", Hidden: true, Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		a, err := updater.ReadAttempt(args[0])
		if err != nil {
			return err
		}
		err = updater.OfficialEvent(a, args[1], args[2])
		if args[1] == "restart" || args[2] == "failed" {
			reportCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			updater.Flush(reportCtx, updater.NewClient(a.Identity), a.Identity)
		}
		return err
	}}
	c.AddCommand(check, install, register, apply, begin, report)
}
func updateCheck(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	id, err := updater.NewIdentity(ctx.Paths)
	if err != nil {
		return nil, err
	}
	client := updater.NewClient(id)
	offer, err := client.Check(context.Background(), id, flagStr(cmd, "version"))
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "component": "CLI", "currentVersion": id.CurrentVersion, "platform": id.Platform, "arch": id.Arch, "updateAvailable": offer != nil, "artifact": offer, "scheduled": false}
	if offer == nil || flagBool(cmd, "check-only") || cmd.Name() == "self" {
		return result, nil
	}
	runtime, err := inspectUpdateRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if !runtime.Running {
		result["message"] = "发现新版，但 daemon 未运行，未安排自动更新"
		return result, nil
	}
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return nil, err
	}
	if !cfg.AutoUpdate.Enabled {
		result["message"] = "发现新版，但自动更新已关闭，未安排自动更新"
		return result, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	target, err := updater.OwnedTarget(paths.RootDir(), exe)
	if err != nil {
		return nil, err
	}
	if runtime.Version != id.CurrentVersion || runtime.Executable != target || runtime.Profile != id.Profile || "https://"+runtime.Host != id.BaseURL {
		return nil, fmt.Errorf("运行中的 daemon 与当前 CLI 版本、路径或环境不一致，请先重启 daemon")
	}
	due, err := updater.ScheduleCheck(id, *offer, time.Now())
	if err != nil {
		return nil, err
	}
	result["scheduled"], result["notBefore"] = true, due
	result["message"] = "已安排至少 10 分钟后空闲安装；已清除失败冷却，重复检查不延长倒计时，录音业务繁忙时将延后"
	return result, nil
}
func updateInstall(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if _, err := updater.OwnedTarget(paths.RootDir(), exe); err != nil {
		return nil, err
	}
	if _, err := inspectUpdateRuntime(ctx); err != nil {
		return nil, err
	}
	id, err := updater.NewIdentity(ctx.Paths)
	if err != nil {
		return nil, err
	}
	client := updater.NewClient(id)
	offer, err := client.Check(context.Background(), id, flagStr(cmd, "version"))
	if err != nil {
		return nil, err
	}
	if offer == nil {
		return map[string]any{"ok": true, "updateAvailable": false}, nil
	}
	updateID, err := updater.Launch(id, *offer, "cli", exe)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "updateId": updateID, "targetVersion": offer.TargetVersion, "status": "started", "message": "更新已交给独立进程，started 不代表安装成功"}, nil
}
func inspectUpdateRuntime(ctx *clictx.Context) (updater.Runtime, error) {
	owner, err := environmentOwner(ctx)
	if err != nil {
		return updater.Runtime{}, err
	}
	if owner != "none" && owner != "standalone-daemon" {
		return updater.Runtime{}, errs.New(errs.CodeDaemonDisabledByPlugin, "当前由 Hermes 或其他宿主持有运行权，更新不会接管")
	}
	for _, name := range paths.ListProfileNames() {
		if name != ctx.Profile && daemon.State(paths.For(name)).Running {
			return updater.Runtime{}, fmt.Errorf("另一个 profile %s 正在运行，暂不替换共享二进制", name)
		}
	}
	state := daemon.State(ctx.Paths)
	if !state.Running {
		return updater.Runtime{}, nil
	}
	code, raw, err := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
	if err != nil {
		return updater.Runtime{}, err
	}
	body, ok := raw.(map[string]any)
	if !ok || code != 200 {
		return updater.Runtime{}, fmt.Errorf("无法确认 daemon 状态")
	}
	activity, _ := body["activity"].(map[string]any)
	busy, known := activity["busy"].(bool)
	if !known {
		return updater.Runtime{}, fmt.Errorf("旧 daemon 不支持忙碌检查，请先停止 daemon 再更新")
	}
	relay, _ := body["relay"].(map[string]any)
	cloud, _ := body["cloud"].(map[string]any)
	connected, _ := relay["connected"].(bool)
	ver, _ := body["version"].(string)
	exe, _ := body["executable"].(string)
	profile, _ := body["profile"].(string)
	host, _ := cloud["host"].(string)
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
		if canonical, e := updater.OwnedTarget(paths.RootDir(), exe); e == nil {
			exe = canonical
		}
	}
	return updater.Runtime{Running: true, Busy: busy, Connected: connected, Version: ver, Executable: exe, Profile: profile, Host: host}, nil
}
func updateApply(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	a, err := updater.ReadAttempt(args[0])
	if err != nil {
		return nil, err
	}
	if a.Identity.Root != paths.RootDir() || a.Identity.Profile != ctx.Profile {
		return nil, fmt.Errorf("更新上下文与当前数据目录不一致")
	}
	release, err := daemon.AcquireControlLock()
	if err != nil {
		return nil, err
	}
	defer release()
	invoke := func(args ...string) error {
		callCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		command := exec.CommandContext(callCtx, a.Target, append([]string{"--profile", ctx.Profile}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			if callCtx.Err() != nil {
				return &updater.Error{Code: "CLI_COMMAND_TIMEOUT", Message: "CLI 命令执行超时"}
			}
			return updater.CommandFailure("CLI "+args[0]+" 执行失败", output)
		}
		return nil
	}
	err = updater.Run(context.Background(), updater.NewClient(a.Identity), a, updater.Hooks{
		Inspect: func() (updater.Runtime, error) { return inspectUpdateRuntime(ctx) },
		Stop:    func() error { return invoke("daemon", "stop") },
		Start:   func() error { return invoke("daemon", "start") },
		Skills: func() error {
			for _, target := range skills.Targets() {
				if target.Agent == "claude" && target.Detected {
					return invoke("skills", "install", "--agent", "claude", "--force")
				}
			}
			return nil
		},
	})
	return map[string]any{"ok": err == nil, "updateId": a.UpdateID}, err
}

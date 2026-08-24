package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/spf13/cobra"
)

const ownerHandoffTimeout = 20 * time.Second

var (
	findExecutable = exec.LookPath
	runExecutable  = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

func newOwnerCmd() *cobra.Command {
	c := &cobra.Command{Use: "owner", Short: "Relay owner 交接"}
	activate := &cobra.Command{
		Use:   "activate cli",
		Short: "停用 Hermes YoooClaw 插件并让 standalone CLI 接管 Relay",
		Args:  cobra.ExactArgs(1),
		RunE:  run(ownerActivate),
	}
	activate.Flags().String("hermes-profile", "", "要停用 YoooClaw 插件的 Hermes profile")
	activate.Flags().Bool("no-start", false, "只释放 Hermes owner，不启动 CLI daemon")
	c.AddCommand(activate)
	return c
}

func ownerActivate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	if args[0] != "cli" {
		return nil, errs.New(errs.CodeInvalidArgument, "当前只支持激活 cli owner")
	}
	return activateCLIOwner(ctx, flagStr(cmd, "hermes-profile"), !flagBool(cmd, "no-start"))
}

func activateCLIOwner(ctx *clictx.Context, hermesProfile string, start bool) (map[string]any, error) {
	stoppedProfiles, err := stopAllProfileDaemons(ctx.Profile)
	if err != nil {
		return nil, err
	}

	heldBefore, ownerBefore, err := daemon.WriterLockStatus(ctx.Paths)
	if err != nil {
		return nil, errs.New(errs.CodeStorageUnavailable, "无法检查当前 writer owner："+err.Error())
	}
	relayHeldBefore, err := daemon.RelayConsumerLockHeld(ctx.Paths)
	if err != nil {
		return nil, errs.New(errs.CodeStorageUnavailable, "无法检查当前 Relay owner："+err.Error())
	}
	runtimeHeldBefore := heldBefore || relayHeldBefore

	hermesBin, _ := findHermesExecutable()
	disabled := []string{}
	disableErrors := []string{}
	if hermesBin != "" {
		for _, plugin := range []string{"yoooclaw", "yoooclaw_app"} {
			out, runErr := runExecutable(hermesBin, hermesArgs(hermesProfile, "plugins", "disable", plugin)...)
			if runErr == nil {
				disabled = append(disabled, plugin)
				continue
			}
			disableErrors = append(disableErrors, plugin+": "+compactCommandError(out, runErr))
		}
	}

	// Disabling a plugin changes the next Hermes process only. If a live
	// runtime owns writer.lock or the account Relay lock, one controlled
	// gateway restart is required to unload it while preserving every
	// non-YoooClaw Hermes channel.
	restartedGateway := false
	if runtimeHeldBefore {
		if hermesBin == "" {
			return nil, ownerHandoffError(ownerBefore, "找不到 hermes 命令，无法让正在运行的插件释放 Relay")
		}
		if len(disableErrors) > 0 {
			return nil, ownerHandoffError(ownerBefore, "停用 Hermes YoooClaw 插件失败："+strings.Join(disableErrors, "; "))
		}
		out, runErr := runExecutable(hermesBin, hermesArgs(hermesProfile, "gateway", "restart")...)
		if runErr != nil {
			return nil, ownerHandoffError(ownerBefore, "Hermes Gateway 重启失败："+compactCommandError(out, runErr))
		}
		restartedGateway = true
	}

	if err := waitForOwnerRelease(ctx.Paths, ownerHandoffTimeout); err != nil {
		return nil, err
	}

	result := map[string]any{
		"ok":               true,
		"owner":            "standalone-daemon",
		"profile":          ctx.Profile,
		"stoppedProfiles":  stoppedProfiles,
		"hermesExecutable": ownerNilIfEmpty(hermesBin),
		"disabledPlugins":  disabled,
		"gatewayRestarted": restartedGateway,
		"daemonStarted":    false,
	}
	if len(disableErrors) > 0 {
		// No live Hermes lock existed, so these errors do not block the current
		// takeover. Keep them visible: a later Hermes launch could otherwise
		// reclaim ownership unexpectedly.
		result["warnings"] = disableErrors
	}

	if !start {
		result["activation"] = "released"
		return result, nil
	}
	if !config.Exists(ctx.Paths) {
		result["activation"] = "pending-config"
		result["hint"] = "运行 `yoooclaw config init`；配置完成后 daemon 会自动启动"
		return result, nil
	}
	lock, err := startStandalone(ctx)
	if err != nil {
		return nil, errs.New(errs.CodeUnknown, "CLI owner 已释放，但 standalone daemon 启动失败："+err.Error()).
			WithHint("检查 `yoooclaw daemon logs`，修复后运行 `yoooclaw daemon start`")
	}
	result["activation"] = "active"
	result["daemonStarted"] = true
	if lock != nil && lock.PID > 0 {
		result["pid"] = lock.PID
	}
	return result, nil
}

func stopAllProfileDaemons(activeProfile string) ([]string, error) {
	names := paths.ListProfileNames()
	if len(names) == 0 {
		names = []string{activeProfile}
	}
	seen := map[string]bool{}
	stopped := []string{}
	for _, profile := range append(names, activeProfile) {
		if profile == "" || seen[profile] {
			continue
		}
		seen[profile] = true
		p := paths.For(profile)
		state := daemon.State(p)
		if !state.Running {
			if state.Stale {
				daemon.RemoveLock(p)
			}
			continue
		}
		if _, err := daemon.Stop(p); err != nil {
			return nil, errs.New(errs.CodeDaemonUnresponsive,
				fmt.Sprintf("无法停止 profile %s 的旧 daemon：%s", profile, err.Error()),
				map[string]any{"profile": profile, "pid": state.Lock.PID}).
				WithHint("旧 daemon 未确认退出，安装器不会继续造成双 owner")
		}
		stopped = append(stopped, profile)
	}
	return stopped, nil
}

func waitForOwnerRelease(p paths.Paths, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastOwner string
	for {
		writerHeld, owner, writerErr := daemon.WriterLockStatus(p)
		relayHeld, relayErr := daemon.RelayConsumerLockHeld(p)
		if writerErr != nil {
			return errs.New(errs.CodeStorageUnavailable, "无法检查 writer lock："+writerErr.Error())
		}
		if relayErr != nil {
			return errs.New(errs.CodeStorageUnavailable, "无法检查 Relay lock："+relayErr.Error())
		}
		lastOwner = owner
		if !writerHeld && !relayHeld {
			return nil
		}
		if time.Now().After(deadline) {
			return ownerHandoffError(lastOwner, "等待 writer/Relay owner 释放超时")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func findHermesExecutable() (string, error) {
	if found, err := findExecutable("hermes"); err == nil {
		return found, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "hermes"),
		filepath.Join(home, ".local", "bin", "hermes.exe"),
		filepath.Join(home, ".hermes", "bin", "hermes"),
		filepath.Join(home, ".hermes", "bin", "hermes.exe"),
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "hermes", "hermes-agent", "venv", "Scripts", "hermes.exe"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func hermesArgs(profile string, args ...string) []string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("HERMES_PROFILE"))
	}
	if profile == "" || profile == "default" {
		return args
	}
	return append([]string{"--profile", profile}, args...)
}

func compactCommandError(output []byte, err error) string {
	text := strings.Join(strings.Fields(string(output)), " ")
	if text == "" {
		return err.Error()
	}
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return text
}

func ownerHandoffError(owner, message string) error {
	return errs.New(errs.CodeDaemonDisabledByPlugin, message,
		map[string]any{"owner": owner}).
		WithHint("确认 hermes 可执行后重试；若命令正由 Hermes 会话执行，请退出该会话再运行安装命令")
}

func ownerNilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

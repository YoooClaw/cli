package cli

import (
	"fmt"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/envhost"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/spf13/cobra"
	"time"
)

func newEnvCmd() *cobra.Command {
	c := &cobra.Command{Use: "env", Short: "查看或切换共享云端环境"}
	c.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: run(envShow)})
	c.AddCommand(&cobra.Command{Use: "switch <environment>", Short: "切换 production / test / development（不接管、不修改凭证）", Args: cobra.ExactArgs(1), RunE: run(envSwitch)})
	return c
}

// A live OS lock is authoritative; metadata alone cannot prove ownership.
func environmentOwner(ctx *clictx.Context) (string, error) {
	names := append(paths.ListProfileNames(), ctx.Profile)
	cliRunning := false
	for _, name := range names {
		held, _, err := writerLockStatus(paths.For(name))
		if err != nil {
			return "", err
		}
		if held {
			return "hermes-plugin", nil
		}
		st := daemon.State(paths.For(name))
		if st.ReadError != "" {
			return "", fmt.Errorf("无法确认 daemon 状态: %s", st.ReadError)
		}
		if st.Running {
			if st.Lock.Owner != "" && st.Lock.Owner != "standalone-daemon" {
				return st.Lock.Owner, nil
			}
			cliRunning = true
		}
	}
	held, err := relayConsumerLockHeld(ctx.Paths)
	if err != nil {
		return "", err
	}
	if held && !cliRunning {
		return "unknown", nil
	}
	if cliRunning {
		return "standalone-daemon", nil
	}
	return "none", nil
}

func envShow(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return nil, err
	}
	owner, err := environmentOwner(ctx)
	if err != nil {
		return nil, err
	}
	host, source := config.ResolveCloudHostSource(cfg)
	result := map[string]any{"ok": true, "profile": ctx.Profile, "owner": owner, "env": envhost.NameForHost(host), "host": host, "source": source, "relayUrl": config.ResolveRelayURL(cfg), "sharedEnvironment": true, "runtime": nil}
	if daemon.State(ctx.Paths).Running {
		status, body, err := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("daemon 状态检查失败: HTTP %d", status)
		}
		result["runtime"] = body
	}
	return result, nil
}

func envSwitch(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	host, valid := envhost.DefaultHost(args[0])
	if !valid {
		return nil, errs.New(errs.CodeInvalidArgument, "环境必须是 production、test 或 development")
	}
	release, err := daemon.AcquireControlLock()
	if err != nil {
		return nil, fmt.Errorf("其他环境切换或接管正在执行: %w", err)
	}
	defer release()
	owner, err := environmentOwner(ctx)
	if err != nil {
		return nil, err
	}
	if owner != "none" && owner != "standalone-daemon" {
		return nil, errs.New(errs.CodeDaemonDisabledByPlugin, "当前 owner 为 "+owner+"；未修改环境，请使用 Hermes 的环境命令")
	}
	if owner == "none" {
		releaseRelay, err := daemon.ReserveRelay(ctx.Paths)
		if err != nil {
			return nil, err
		}
		defer releaseRelay()
	}
	cfg, err := config.Require(ctx.Paths)
	if err != nil {
		return nil, err
	}
	if envhost.Explicit() && envhost.Host() != host {
		return nil, errs.New(errs.CodeInvalidArgument, "环境变量覆盖与目标环境冲突，请先处理 PHONE_NOTIFICATIONS_ENV / OPENCLAW_HOST_*；未修改配置")
	}
	if cfg.Relay.URL != "" && envhost.NameForHost(cfg.Relay.URL) == "custom" {
		return nil, errs.New(errs.CodeInvalidArgument, "当前配置为自定义 Relay 地址，请先处理该配置；未修改环境")
	}
	st := daemon.State(ctx.Paths)
	if st.Running {
		status, body, err := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
		if err != nil {
			return nil, err
		}
		payload, _ := body.(map[string]any)
		cloud, _ := payload["cloud"].(map[string]any)
		if cloud["source"] == "env" && cloud["host"] != host {
			return nil, errs.New(errs.CodeInvalidArgument, "运行中的 daemon 有冲突的环境覆盖，请先处理其启动环境；未修改配置")
		}
		activity, _ := payload["activity"].(map[string]any)
		busy, known := activity["busy"].(bool)
		if status != 200 || !known {
			return nil, errs.New(errs.CodeInvalidArgument, "运行中的 daemon 不支持忙碌检查，请先更新 CLI 并重启 daemon；未修改配置")
		}
		if busy {
			return nil, errs.New(errs.CodeInvalidArgument, "正在同步或处理请求，请稍后切换环境；未修改配置")
		}
		if ctx.Profile != persistentActiveProfile() {
			if managed, _ := serviceManaged(); managed {
				return nil, errs.New(errs.CodeInvalidArgument, "非活动 profile 不重启共享自启服务；未修改配置")
			}
		}
	}
	raw, err := config.LoadRaw(ctx.Paths)
	if err != nil {
		return nil, err
	}
	config.SetByPath(raw, "cloud.host", host)
	config.SetByPath(raw, "relay.url", "wss://"+host+config.RelayPath)
	if err := config.SaveRaw(ctx.Paths, raw); err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "configured": true, "env": args[0], "host": host, "profile": ctx.Profile, "owner": owner, "sharedEnvironment": true, "restarted": false, "connected": false}
	if !st.Running {
		return result, nil
	}
	restart := newDaemonCmd()
	cmd, _, _ := restart.Find([]string{"restart"})
	if _, err := daemonRestart(ctx, cmd, nil); err != nil {
		return nil, errs.New(errs.CodeUnknown, "环境配置已保存，但 daemon 重启失败", map[string]any{"configured": true, "env": args[0]}).WithHint("检查 yc daemon status 和日志后重试")
	}
	result["restarted"] = true
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, body, reqErr := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
		if reqErr == nil {
			payload, _ := body.(map[string]any)
			cloud, _ := payload["cloud"].(map[string]any)
			relay, _ := payload["relay"].(map[string]any)
			if cloud["host"] == host && relay["connected"] == true && relay["currentUrl"] == "wss://"+host+config.RelayPath {
				result["connected"] = true
				return result, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, errs.New(errs.CodeNetworkError, "配置已保存、daemon 已重启，但目标环境连接尚未确认", map[string]any{"configured": true, "restarted": true, "env": args[0]}).WithHint("检查 yc env show、目标环境 API Key 和网络；未自动回退环境")
}

package cli

import (
	"os"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	c := &cobra.Command{Use: "daemon", Short: "守护进程管理 🔵"}

	start := &cobra.Command{Use: "start", Short: "启动 daemon（默认后台 detach）", Args: cobra.NoArgs, RunE: run(daemonStart)}
	start.Flags().String("bind", "", "监听地址（默认 config.daemon.bind）")
	start.Flags().String("port", "", "监听端口（默认 config.daemon.port）")
	start.Flags().Bool("no-detach", false, "前台运行（systemd/launchd 用）")
	start.Flags().String("log-level", "", "error|warn|info|debug|trace")

	stop := &cobra.Command{Use: "stop", Short: "停止 daemon（SIGTERM → 10s → SIGKILL）", Args: cobra.NoArgs, RunE: run(daemonStop)}
	restart := &cobra.Command{Use: "restart", Short: "stop + start，保留原启动参数", Args: cobra.NoArgs, RunE: run(daemonRestart)}
	restart.Flags().String("bind", "", "监听地址")
	restart.Flags().String("port", "", "监听端口")
	restart.Flags().Bool("no-detach", false, "前台运行")
	restart.Flags().String("log-level", "", "日志级别")
	reload := &cobra.Command{Use: "reload", Short: "重读凭据并增量刷新 Relay 隧道", Args: cobra.NoArgs, RunE: run(daemonReload)}
	status := &cobra.Command{Use: "status", Short: "打印 daemon 状态（PID/端口/relay/规则数...）", Args: cobra.NoArgs, RunE: run(daemonStatus)}

	logs := &cobra.Command{Use: "logs", Short: "跟踪 daemon 日志", Args: cobra.NoArgs, RunE: run(daemonLogs)}
	logs.Flags().BoolP("follow", "f", false, "持续 tail")
	logs.Flags().String("lines", "100", "初始展示行数")
	logs.Flags().String("level", "", "过滤日志级别")

	runFg := &cobra.Command{Use: "run-foreground", Short: "（内部）前台运行 daemon 主循环", Args: cobra.NoArgs, RunE: run(daemonRunForeground)}
	runFg.Flags().String("bind", "", "监听地址")
	runFg.Flags().String("port", "", "监听端口")
	runFg.Flags().String("log-level", "", "日志级别")

	c.AddCommand(start, stop, restart, reload, status, logs, runFg)
	return c
}

func startOptsFromCmd(cmd *cobra.Command) daemon.StartOpts {
	port := 0
	if s := flagStr(cmd, "port"); s != "" {
		port, _ = strconv.Atoi(s)
	}
	return daemon.StartOpts{Bind: flagStr(cmd, "bind"), Port: port, LogLevel: flagStr(cmd, "log-level")}
}

func daemonRunForeground(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if err := daemon.RunForeground(ctx, startOptsFromCmd(cmd)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func daemonStart(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if _, err := config.Require(ctx.Paths); err != nil {
		return nil, err
	}
	state := daemon.State(ctx.Paths)
	if state.Running {
		return nil, errs.New(errs.CodeDaemonAlreadyRunning, "daemon 已在运行（pid "+strconv.Itoa(state.Lock.PID)+"）",
			map[string]any{"pid": state.Lock.PID})
	}
	if state.Stale {
		daemon.RemoveLock(ctx.Paths)
	}
	opts := startOptsFromCmd(cmd)
	if flagBool(cmd, "no-detach") {
		if err := daemon.RunForeground(ctx, opts); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	lock, err := daemon.Spawn(ctx, opts)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "pid": lock.PID, "bind": lock.Bind, "port": lock.Port, "detached": true}, nil
}

func daemonStop(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return daemon.Stop(ctx.Paths)
}

func daemonRestart(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	lock := daemon.ReadLock(ctx.Paths)
	opts := startOptsFromCmd(cmd)
	if lock != nil {
		if opts.Bind == "" {
			opts.Bind = lock.Bind
		}
		if opts.Port == 0 {
			opts.Port = lock.Port
		}
		if opts.LogLevel == "" {
			opts.LogLevel = lock.LogLevel
		}
		if daemon.IsProcessAlive(lock.PID) {
			if _, err := daemon.Stop(ctx.Paths); err != nil {
				return nil, err
			}
		}
	}
	if _, err := config.Require(ctx.Paths); err != nil {
		return nil, err
	}
	if flagBool(cmd, "no-detach") {
		if err := daemon.RunForeground(ctx, opts); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	newLock, err := daemon.Spawn(ctx, opts)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "pid": newLock.PID, "bind": newLock.Bind, "port": newLock.Port, "detached": true}, nil
}

func daemonReload(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	state := daemon.State(ctx.Paths)
	if !state.Running {
		return map[string]any{"ok": true, "running": false, "reloaded": false}, nil
	}
	_, body, err := daemon.NewClient(ctx.Paths).Request("POST", "/daemon/reload", nil)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func daemonStatus(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	state := daemon.State(ctx.Paths)
	if !state.Running {
		return nil, errs.New(errs.CodeDaemonNotRunning, "daemon 未运行",
			map[string]any{"stale": state.Stale, "hint": "yoooclaw daemon start"})
	}
	_, body, err := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func daemonLogs(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	n := atoiDefault(flagStr(cmd, "lines"), 100)
	lines := tailLines(ctx.Paths.DaemonLog, n)
	if level := flagStr(cmd, "level"); level != "" {
		want := "[" + strings.ToUpper(level) + "]"
		filtered := lines[:0]
		for _, l := range lines {
			if strings.Contains(l, want) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}
	// --follow 的实时 tail 暂不实现（Phase 2 标准输出即可）；返回当前快照。
	return map[string]any{"ok": true, "file": ctx.Paths.DaemonLog, "total": len(lines), "lines": lines}, nil
}

func tailLines(file string, n int) []string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return []string{}
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

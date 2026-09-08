package cli

import (
	"context"
	"fmt"
	"github.com/YoooClaw/cli/internal/diagnostics"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	c := &cobra.Command{Use: "daemon", Short: "守护进程管理 🔵"}

	start := &cobra.Command{Use: "start", Short: "启动 daemon（自启已启用时由系统用户服务托管）", Args: cobra.NoArgs, RunE: run(daemonStart)}
	start.Flags().String("bind", "", "监听地址（默认 config.daemon.bind）")
	start.Flags().String("port", "", "监听端口（默认 config.daemon.port）")
	start.Flags().Bool("no-detach", false, "前台运行（systemd/launchd 用）")
	start.Flags().String("log-level", "", "error|warn|info|debug|trace")
	addDaemonLifecycleFlags(start)
	addIngressFlags(start)

	stop := &cobra.Command{Use: "stop", Short: "停止 daemon（SIGTERM → 10s → SIGKILL）", Args: cobra.NoArgs, RunE: run(daemonStop)}
	stop.Flags().String("owner", "", "仅停止 owner 匹配的 daemon")
	stop.Flags().String("generation", "", "仅停止 generation 匹配的 daemon")
	stop.Flags().Bool("wait", true, "等待 daemon 完全退出")
	restart := &cobra.Command{Use: "restart", Short: "stop + start，保留原启动参数", Args: cobra.NoArgs, RunE: run(daemonRestart)}
	restart.Flags().String("bind", "", "监听地址")
	restart.Flags().String("port", "", "监听端口")
	restart.Flags().Bool("no-detach", false, "前台运行")
	restart.Flags().String("log-level", "", "日志级别")
	addDaemonLifecycleFlags(restart)
	addIngressFlags(restart)
	reload := &cobra.Command{Use: "reload", Short: "重读凭据并增量刷新 Relay 隧道", Args: cobra.NoArgs, RunE: run(daemonReload)}
	status := &cobra.Command{Use: "status", Short: "打印 daemon 状态（PID/端口/relay/规则数...）", Args: cobra.NoArgs, RunE: run(daemonStatus)}

	logs := &cobra.Command{Use: "logs", Short: "跟踪 daemon 日志", Args: cobra.NoArgs, RunE: run(daemonLogs)}
	logs.Flags().BoolP("follow", "f", false, "持续 tail")
	logs.Flags().String("lines", "100", "初始展示行数")
	logs.Flags().String("level", "", "过滤日志级别")
	logs.Flags().Bool("supervisor", false, "查看系统用户服务启动日志")
	logs.Flags().Bool("diagnostics", false, "汇总 daemon/自启日志、进程状态和 Linux 本次启动的系统服务记录")

	runFg := &cobra.Command{Use: "run-foreground", Short: "（内部）前台运行 daemon 主循环", Args: cobra.NoArgs, RunE: run(daemonRunForeground)}
	runFg.Flags().String("bind", "", "监听地址")
	runFg.Flags().String("port", "", "监听端口")
	runFg.Flags().String("log-level", "", "日志级别")
	addDaemonLifecycleFlags(runFg)
	addIngressFlags(runFg)
	runService := &cobra.Command{Use: "run-service", Short: "由系统用户服务运行 daemon", Hidden: true, Args: cobra.NoArgs, RunE: run(daemonRunService)}
	runService.Flags().String("root", "", "（内部）服务数据根目录")

	c.AddCommand(start, stop, restart, reload, status, logs, runFg, runService, newDaemonAutostartCmd())
	return c
}

func startOptsFromCmd(cmd *cobra.Command) daemon.StartOpts {
	port := 0
	if s := flagStr(cmd, "port"); s != "" {
		port, _ = strconv.Atoi(s)
	}
	return daemon.StartOpts{
		Bind: flagStr(cmd, "bind"), Port: port, LogLevel: flagStr(cmd, "log-level"),
		Owner: flagStr(cmd, "owner"), Generation: flagStr(cmd, "generation"),
		IngressMode:         flagStr(cmd, "ingress"),
		EgressCallbackURL:   flagStr(cmd, "egress-callback-url"),
		EgressCallbackToken: flagStr(cmd, "egress-callback-token"),
	}
}

func addDaemonLifecycleFlags(cmd *cobra.Command) {
	cmd.Flags().String("owner", "", "生命周期 owner（例如 hermes-plugin）")
	cmd.Flags().String("generation", "", "生命周期 generation，用于识别同一批启动的进程")
}

func addIngressFlags(cmd *cobra.Command) {
	cmd.Flags().String("ingress", "", "传输模式 standalone|proxied|direct（默认 config.ingress.mode）")
	cmd.Flags().String("egress-callback-url", "", "proxied 模式出站事件回投宿主的 URL")
	cmd.Flags().String("egress-callback-token", "", "proxied 模式出站回调的 Bearer token")
}

func stopOptsFromCmd(cmd *cobra.Command) daemon.StopOpts {
	return daemon.StopOpts{Owner: flagStr(cmd, "owner"), Generation: flagStr(cmd, "generation"), Wait: flagBool(cmd, "wait")}
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
	if err := rejectManagedOverrides(cmd); err != nil {
		return nil, err
	}
	opts := startOptsFromCmd(cmd)
	// detach 模式下子进程失败只会表现为"等 lock 超时"；先在父进程做
	// 写者锁预检，把 YOOOCLAW_DAEMON_DISABLED_BY_PLUGIN 直接抛给调用方。
	if err := daemon.PrecheckStart(ctx, opts); err != nil {
		return nil, err
	}
	if flagBool(cmd, "no-detach") {
		if err := daemon.RunForeground(ctx, opts); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	if managed, _ := serviceManaged(); managed {
		lock, err := startStandalone(ctx)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"ok": true, "supervised": true, "autostart": true}
		if lock != nil && lock.PID > 0 {
			result["pid"], result["bind"], result["port"] = lock.PID, lock.Bind, lock.Port
		}
		return result, nil
	}
	lock, err := daemon.Spawn(ctx, opts)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "pid": lock.PID, "bind": lock.Bind, "port": lock.Port, "detached": true}, nil
}

func daemonStop(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	return stopManagedDaemon(ctx, stopOptsFromCmd(cmd))
}

func daemonRestart(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if err := rejectManagedOverrides(cmd); err != nil {
		return nil, err
	}
	if _, err := config.Require(ctx.Paths); err != nil {
		return nil, err
	}
	if managed, serviceStatus := serviceManaged(); managed {
		if !serviceStatus.Running {
			if err := daemon.PrecheckStart(ctx, daemon.StartOpts{}); err != nil {
				return nil, err
			}
		}
		return restartManagedDaemon(ctx)
	}
	state := daemon.State(ctx.Paths)
	lock := state.Lock
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
		if state.Running {
			if _, err := daemon.Stop(ctx.Paths); err != nil {
				return nil, err
			}
		}
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

func logDaemonObservation(ctx *clictx.Context, event string, state daemon.RunningState) {
	fields := map[string]any{"profile": ctx.Profile, "root": paths.RootDir(), "lockPath": ctx.Paths.DaemonLock, "running": state.Running, "stale": state.Stale, "reason": state.Reason, "lockReadError": state.ReadError, "actualExecutable": state.ActualExecutable}
	if state.Lock != nil {
		fields["lockPID"] = state.Lock.PID
		fields["expectedExecutable"] = state.Lock.Executable
		fields["lockProfile"] = state.Lock.Profile
		fields["lockVersion"] = state.Lock.Version
		fields["port"] = state.Lock.Port
	}
	status, err := autostartManager().Status()
	fields["service"] = status
	if err != nil {
		fields["serviceError"] = diagnostics.SafeError(err)
	}
	level := "info"
	if !state.Running {
		level = "warn"
	}
	diagnostics.Write(paths.RootDir(), level, event, fields)
}

func daemonReload(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	state := daemon.State(ctx.Paths)
	logDaemonObservation(ctx, "reload.request", state)
	if !state.Running {
		return map[string]any{"ok": true, "running": false, "reloaded": false, "stale": state.Stale,
			"reason": "未发现可识别的 daemon 进程，未执行配置重载",
			"hint":   "运行 yoooclaw daemon start；若系统服务显示运行中，请检查 daemon.lock 与进程身份是否一致"}, nil
	}
	_, body, err := daemon.NewClient(ctx.Paths).Request("POST", "/daemon/reload", nil)
	if err != nil {
		diagnostics.Write(paths.RootDir(), "error", "reload.request_failed", map[string]any{"profile": ctx.Profile, "error": diagnostics.SafeError(err)})
		return nil, err
	}
	diagnostics.Write(paths.RootDir(), "info", "reload.response_received", map[string]any{"profile": ctx.Profile})
	return body, nil
}

func daemonStatus(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	state := daemon.State(ctx.Paths)
	logDaemonObservation(ctx, "status.observed", state)
	if !state.Running {
		return nil, errs.New(errs.CodeDaemonNotRunning, "daemon 未运行",
			map[string]any{"stale": state.Stale, "reason": state.Reason, "hint": "yoooclaw daemon start"})
	}
	_, body, err := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
	if err != nil {
		return nil, err
	}
	if m, ok := body.(map[string]any); ok {
		if supervision, snapshotErr := autostartSnapshot(); snapshotErr == nil {
			m["supervision"] = supervision
		}
	}
	return body, nil
}

func daemonLogs(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	n := atoiDefault(flagStr(cmd, "lines"), 100)
	if n < 1 || n > 1000 {
		return nil, errs.New(errs.CodeInvalidArgument, "--lines 必须在 1 到 1000 之间")
	}
	file := ctx.Paths.DaemonLog
	if flagBool(cmd, "supervisor") {
		file = filepath.Join(paths.RootDir(), "logs", "daemon-supervisor.log")
	}
	lines := tailLines(file, n)
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
	result := map[string]any{"ok": true, "file": file, "total": len(lines), "lines": lines}
	if flagBool(cmd, "diagnostics") {
		supervisorFile := filepath.Join(paths.RootDir(), "logs", "daemon-supervisor.log")
		result["supervisor"] = map[string]any{"file": supervisorFile, "lines": tailLines(supervisorFile, n)}
		result["process"] = daemon.State(ctx.Paths)
		result["root"] = paths.RootDir()
		result["profile"] = ctx.Profile
		result["configPath"] = ctx.Paths.Config
		status, err := autostartSnapshot()
		result["autostart"] = status
		if err != nil {
			result["autostartError"] = diagnostics.SafeError(err)
		}
		native, _ := autostartManager().Status()
		if runtime.GOOS == "linux" && native.Manager == "systemd" && native.Unit != "" {
			result["systemd"] = readServiceDiagnostic("systemctl", "--user", "show", native.Unit, "--property=ActiveState,SubState,Result,ExecMainCode,ExecMainStatus,ExecMainStartTimestamp,UnitFileState,InvocationID")
			result["userManager"] = readServiceDiagnostic("systemctl", "show", fmt.Sprintf("user@%d.service", os.Getuid()), "--property=ActiveState,SubState,Result,ExecMainStartTimestamp")
			result["userManagerJournal"] = readServiceDiagnostic("journalctl", "-b", "-u", fmt.Sprintf("user@%d.service", os.Getuid()), "--no-pager", "-n", strconv.Itoa(n))
			result["journal"] = readServiceDiagnostic("journalctl", "--user", "-b", "-u", native.Unit, "--no-pager", "-n", strconv.Itoa(n))
		}
	}
	return result, nil
}

// Bound external diagnostics so an unavailable user bus cannot hang logs.
func readServiceDiagnostic(command string, args ...string) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		lines[i] = diagnostics.SafeError(fmt.Errorf("%s", line))
	}
	result := map[string]any{"output": strings.Join(lines, "\n"), "ok": err == nil}
	if err != nil {
		result["error"] = diagnostics.SafeError(err)
	}
	return result
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

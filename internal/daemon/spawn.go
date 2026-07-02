package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// Spawn fork 一个脱离会话的子进程跑 `daemon run-foreground`，轮询 lock 确认起来（最多 ~3s）。
func Spawn(ctx *clictx.Context, opts StartOpts) (*Lock, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, errs.New(errs.CodeUnknown, "无法定位可执行文件："+err.Error())
	}
	return spawn(ctx.Paths, exe, daemonArgs(ctx.Profile, opts), os.Environ(), true)
}

// SpawnFor 同 Spawn，但以**显式 paths + profile** 入参，不依赖 clictx。
// 它通过 yclib 的 re-exec bootstrap 启动 daemon，因此嵌入 library 的宿主程序
// 无需同时实现 yc 的命令行入口。root 会只注入子进程，不修改宿主环境。
func SpawnFor(p paths.Paths, root, profile string, opts StartOpts) (*Lock, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, errs.New(errs.CodeUnknown, "无法定位可执行文件："+err.Error())
	}
	payload, err := json.Marshal(embeddedLaunch{
		Root: root, Profile: profile, Paths: p, Opts: opts,
	})
	if err != nil {
		return nil, errs.New(errs.CodeUnknown, "编码 daemon 启动参数失败："+err.Error())
	}
	env := replaceEnv(os.Environ(), embeddedDaemonEnv, string(payload))
	env = replaceEnv(env, "YOOOCLAW_HOME", root)
	return spawn(p, exe, []string{"--yclib-daemon-bootstrap"}, env, false)
}

func daemonArgs(profile string, opts StartOpts) []string {
	args := []string{"daemon", "run-foreground", "--profile", profile}
	if opts.Bind != "" {
		args = append(args, "--bind", opts.Bind)
	}
	if opts.Port != 0 {
		args = append(args, "--port", strconv.Itoa(opts.Port))
	}
	if opts.LogLevel != "" {
		args = append(args, "--log-level", opts.LogLevel)
	}
	if opts.Owner != "" {
		args = append(args, "--owner", opts.Owner)
	}
	if opts.Generation != "" {
		args = append(args, "--generation", opts.Generation)
	}
	if opts.IngressMode != "" {
		args = append(args, "--ingress", opts.IngressMode)
	}
	if opts.EgressCallbackURL != "" {
		args = append(args, "--egress-callback-url", opts.EgressCallbackURL)
	}
	if opts.EgressCallbackToken != "" {
		args = append(args, "--egress-callback-token", opts.EgressCallbackToken)
	}
	return args
}

func spawn(p paths.Paths, executable string, args, env []string, release bool) (*Lock, error) {
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = detachSysProcAttr()
	var null *os.File
	if f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		null = f
		cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		if null != nil {
			_ = null.Close()
		}
		return nil, errs.New(errs.CodeUnknown, "fork daemon 失败："+err.Error())
	}
	if null != nil {
		_ = null.Close()
	}
	if release {
		_ = cmd.Process.Release()
	} else {
		// An embedding host is long-lived, unlike the short-lived yc CLI parent.
		// Keep a waiter so a stopped daemon cannot remain a zombie until the host exits.
		go func() { _ = cmd.Wait() }()
	}

	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if st := State(p); st.Running {
			return st.Lock, nil
		}
	}
	return nil, errs.New(errs.CodeUnknown, "daemon 启动超时（3s 内未写出 lock）").
		WithHint("查看 yoooclaw daemon logs 排查")
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

// StopOpts limits which daemon a stop call may target.
type StopOpts struct {
	Owner      string
	Generation string
	Wait       bool
}

// Stop 停止 daemon：unix 发 SIGTERM 走优雅退出，Windows 打 /daemon/stop 端点；
// 10s 内未退则强杀。
func Stop(p paths.Paths) (map[string]any, error) {
	return StopWithOptions(p, StopOpts{Wait: true})
}

// StopWithOptions stops a daemon after optional owner/generation checks.
func StopWithOptions(p paths.Paths, opts StopOpts) (map[string]any, error) {
	lock := ReadLock(p)
	if lock == nil || !isProcessAlive(lock.PID) {
		RemoveLock(p)
		return nil, errs.New(errs.CodeDaemonNotRunning, "daemon 未运行")
	}
	if err := checkLifecycleTarget(lock, opts.Owner, opts.Generation); err != nil {
		return nil, err
	}
	signal := "SIGTERM"
	if runtime.GOOS == "windows" {
		signal = "stop-endpoint"
		_, _, _ = NewClient(p).Request("POST", "/daemon/stop", nil)
	} else {
		_ = terminate(lock.PID)
	}
	if !opts.Wait {
		return map[string]any{"ok": true, "stopping": lock.PID, "signal": signal}, nil
	}
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		current := ReadLock(p)
		if current == nil || current.PID != lock.PID || !isProcessAlive(lock.PID) {
			if current != nil && current.PID == lock.PID {
				RemoveLock(p)
			}
			return map[string]any{"ok": true, "stopped": lock.PID, "signal": signal}, nil
		}
	}
	_ = forceKill(lock.PID)
	if current := ReadLock(p); current != nil && current.PID == lock.PID {
		RemoveLock(p)
	}
	return map[string]any{"ok": true, "stopped": lock.PID, "signal": "SIGKILL"}, nil
}

func checkLifecycleTarget(lock *Lock, owner, generation string) error {
	if owner != "" && lock.Owner != owner {
		return errs.New(
			errs.CodeDaemonLifecycleMismatch,
			"daemon owner 不匹配",
			map[string]any{"expectedOwner": owner, "actualOwner": lock.Owner, "pid": lock.PID},
		)
	}
	if generation != "" && lock.Generation != generation {
		return errs.New(
			errs.CodeDaemonLifecycleMismatch,
			"daemon generation 不匹配",
			map[string]any{"expectedGeneration": generation, "actualGeneration": lock.Generation, "pid": lock.PID},
		)
	}
	return nil
}

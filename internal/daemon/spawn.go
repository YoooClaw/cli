package daemon

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// Spawn fork 一个脱离会话的子进程跑 `daemon run-foreground`，轮询 lock 确认起来（最多 ~3s）。
func Spawn(ctx *clictx.Context, opts StartOpts) (*Lock, error) {
	return SpawnFor(ctx.Paths, ctx.Profile, opts)
}

// SpawnFor 同 Spawn，但以**显式 paths + profile** 入参，不依赖 clictx。
// 供 library（yclib）显式起 daemon 时使用。
func SpawnFor(p paths.Paths, profile string, opts StartOpts) (*Lock, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, errs.New(errs.CodeUnknown, "无法定位可执行文件："+err.Error())
	}
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
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detachSysProcAttr()
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return nil, errs.New(errs.CodeUnknown, "fork daemon 失败："+err.Error())
	}
	_ = cmd.Process.Release()

	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if st := State(p); st.Running {
			return st.Lock, nil
		}
	}
	return nil, errs.New(errs.CodeUnknown, "daemon 启动超时（3s 内未写出 lock）").
		WithHint("查看 yoooclaw daemon logs 排查")
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
		if !isProcessAlive(lock.PID) {
			RemoveLock(p)
			return map[string]any{"ok": true, "stopped": lock.PID, "signal": signal}, nil
		}
	}
	_ = forceKill(lock.PID)
	RemoveLock(p)
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

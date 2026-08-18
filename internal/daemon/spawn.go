package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	if !release {
		// An embedding host is long-lived, unlike the short-lived yc CLI parent.
		// Keep a waiter so a stopped daemon cannot remain a zombie until the host exits.
		go func() { _ = cmd.Wait() }()
	}

	lock, readyErr := waitForDaemonReady(p, cmd.Process.Pid)
	if readyErr == nil {
		if release {
			_ = cmd.Process.Release()
		}
		return lock, nil
	}

	// Do not leave a failed child, zombie, or lock behind for the next retry.
	_ = cmd.Process.Kill()
	if release {
		_ = cmd.Wait()
	}
	if current := ReadLock(p); current != nil && current.PID == cmd.Process.Pid {
		RemoveLock(p)
	}
	return nil, errs.New(errs.CodeUnknown, fmt.Sprintf("daemon 启动失败（%s 内 HTTP 未就绪）：%s", daemonReadyTimeout, readyErr.Error())).
		WithHint("查看 yoooclaw daemon logs 排查")
}

// daemonReadyTimeout 是等待 fork 出的 daemon 就绪的上限。不能太短：WSL 冷启动、
// 杀软扫描、高负载机器上 daemon 起来可能超过 3s，等不到就 kill 子进程会让监管方
// 陷入「反复拉起→反复杀」的重启循环。
const daemonReadyTimeout = 15 * time.Second

func waitForDaemonReady(p paths.Paths, pid int) (*Lock, error) {
	var lastErr error = fmt.Errorf("尚未写出 lock")
	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		lock := ReadLock(p)
		if lock == nil || lock.PID != pid {
			if !isProcessAlive(pid) || !isExpectedDaemonProcess(&Lock{PID: pid}) {
				return nil, fmt.Errorf("daemon 进程已退出")
			}
			continue
		}
		if st := State(p); !st.Running {
			return nil, fmt.Errorf("daemon lock 指向的进程无效")
		}
		probeTimeout := min(250*time.Millisecond, time.Until(deadline))
		if probeTimeout <= 0 {
			break
		}
		if err := probeDaemonHealth(lock, probeTimeout); err == nil {
			return lock, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

func probeDaemonHealth(lock *Lock, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(daemonBaseURL(lock.Bind, lock.Port) + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health 返回 HTTP %d", resp.StatusCode)
	}
	var body struct {
		Server string `json:"server"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Server != "yoooclaw" {
		return fmt.Errorf("health 响应不是 yoooclaw daemon")
	}
	return nil
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
	state := State(p)
	lock := state.Lock
	if lock == nil || !state.Running {
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
		currentState := State(p)
		current := currentState.Lock
		if current == nil || current.PID != lock.PID || !currentState.Running {
			if current != nil && current.PID == lock.PID {
				RemoveLock(p)
			}
			return map[string]any{"ok": true, "stopped": lock.PID, "signal": signal}, nil
		}
	}
	// Revalidate identity before SIGKILL so PID reuse can never make a stale
	// daemon lock terminate an unrelated WSL process.
	if current := State(p); current.Running && current.Lock != nil && current.Lock.PID == lock.PID {
		_ = forceKill(lock.PID)
	}
	// A successful forceKill call is not proof that the process has exited
	// (notably with Windows endpoint/security races). Do not let installers
	// continue with a false "stopped" result while the old owner still holds
	// writer/Relay locks.
	for i := 0; i < 20 && isProcessAlive(lock.PID); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if isProcessAlive(lock.PID) {
		return nil, errs.New(errs.CodeDaemonUnresponsive,
			fmt.Sprintf("daemon 强制停止后仍在运行（pid %d）", lock.PID),
			map[string]any{"pid": lock.PID, "profile": p.Profile}).
			WithHint("查看进程权限和安全软件拦截记录；旧进程退出前不会切换 Relay owner")
	}
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

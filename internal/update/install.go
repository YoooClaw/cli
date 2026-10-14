package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

type Installation struct {
	Executable   string    `json:"executable"`
	Owner        string    `json:"owner"`
	RegisteredAt time.Time `json:"registeredAt"`
}

func markerPath(root string) string { return filepath.Join(root, "update", "installation.json") }
func Register(root, executable string) error {
	exe, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if excludedPath(exe) {
		return fail("INSTALLATION_NOT_OWNED", "不登记 npm 或 Hermes 内置 CLI")
	}
	return fsutil.WriteJSON(markerPath(root), Installation{exe, "standalone-cli", time.Now().UTC()}, 0600)
}
func excludedPath(exe string) bool {
	p := strings.ToLower(filepath.ToSlash(exe))
	return strings.Contains(p, "/node_modules/") || strings.Contains(p, "/hermes-plugin/") || strings.Contains(p, "/hermes_plugin/")
}
func OwnedTarget(root, executable string) (string, error) {
	exe, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	var install Installation
	ok, err := fsutil.ReadJSON(markerPath(root), &install)
	if err != nil || !ok || install.Owner != "standalone-cli" || excludedPath(exe) {
		return "", fail("INSTALLATION_NOT_OWNED", "未找到匹配的独立 CLI 安装记录，请使用官方安装脚本更新")
	}
	if install.Executable != exe {
		// The Windows installer creates yc.exe as a byte-identical copy, not a symlink.
		if runtime.GOOS != "windows" || filepath.Base(exe) != "yc.exe" || filepath.Dir(exe) != filepath.Dir(install.Executable) {
			return "", fail("INSTALLATION_NOT_OWNED", "安装路径与登记不一致")
		}
		a, e1 := fileHash(exe)
		b, e2 := fileHash(install.Executable)
		if e1 != nil || e2 != nil || a != b {
			return "", fail("INSTALLATION_CHANGED", "CLI 别名与登记程序不一致，请重新安装")
		}
		exe = install.Executable
	}
	if external := os.Getenv("YOOOCLAW_CLI_PATH"); external != "" {
		return "", fail("SHARED_INSTALLATION", "显式宿主 CLI 路径不参与自更新")
	}
	if sharedWithHermes(exe) {
		return "", fail("SHARED_INSTALLATION", "Hermes 配置引用了这份 CLI，不自动替换共享程序")
	}
	return exe, nil
}

// Hermes normally ships its own binary. Its explicit .env override can instead
// point to this installation even when the standalone daemon lacks that variable.
func sharedWithHermes(executable string) bool {
	home := os.Getenv("HERMES_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		home = filepath.Join(userHome, ".hermes")
	}
	raw, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != "YOOOCLAW_CLI_PATH" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if value == "" {
			continue
		}
		path, err := filepath.EvalSymlinks(value)
		if err == nil && path == executable {
			return true
		}
	}
	return false
}
func fileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Hash(b), nil
}
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func WorkDir(a Attempt) string {
	return filepath.Join(a.Identity.Root, "update", "attempts", a.UpdateID)
}
func JobPath(a Attempt) string { return filepath.Join(WorkDir(a), "attempt.json") }
func Launch(id Identity, a Artifact, trigger, executable string) (string, error) {
	target, err := OwnedTarget(id.Root, executable)
	if err != nil {
		return "", err
	}
	oldHash, err := fileHash(target)
	if err != nil {
		return "", err
	}
	attempt := Attempt{Identity: id, Artifact: a, UpdateID: ID(), Trigger: trigger, Target: target, OldHash: oldHash, CreatedAt: time.Now().UTC()}
	dir := WorkDir(attempt)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := fsutil.WriteJSON(JobPath(attempt), attempt, 0600); err != nil {
		return "", err
	}
	worker := filepath.Join(dir, "worker")
	if runtime.GOOS == "windows" {
		worker += ".exe"
	}
	if err := copyFile(target, worker); err != nil {
		return "", err
	}
	cmd := exec.Command(worker, "--profile", id.Profile, "update", "_apply", JobPath(attempt))
	detach(cmd)
	log, err := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return attempt.UpdateID, nil
}

type Runtime struct {
	Running    bool
	Busy       bool
	Connected  bool
	Version    string
	Executable string
	Profile    string
	Host       string
}
type Hooks struct {
	Inspect func() (Runtime, error)
	Stop    func() error
	Start   func() error
	Skills  func() error
}

func ReadAttempt(path string) (Attempt, error) {
	var a Attempt
	ok, err := fsutil.ReadJSON(path, &a)
	if err != nil {
		return a, err
	}
	if !ok || !regexpID(a.UpdateID) || filepath.Clean(path) != JobPath(a) {
		return a, fail("INVALID_ATTEMPT", "更新上下文路径无效")
	}
	return a, nil
}
func regexpID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func Run(ctx context.Context, c *Client, a Attempt, h Hooks) (runErr error) {
	release, err := lock(filepath.Join(a.Identity.Root, "update", "install.lock"))
	if err != nil {
		return fail("UPDATE_BUSY", "已有更新正在执行")
	}
	defer release()
	stage := "gate"
	defer func() {
		if runErr != nil {
			Record(a, stage, "failed", runErr)
			Final(a, runErr)
		}
		Flush(context.Background(), c, a.Identity)
	}()
	if _, err := c.Key(); err != nil {
		return err
	}
	if _, err := OwnedTarget(a.Identity.Root, a.Target); err != nil {
		return err
	}
	if hash, err := fileHash(a.Target); err != nil || hash != a.OldHash {
		return fail("INSTALLATION_CHANGED", "当前安装已改变，请重新检查")
	}
	Record(a, "gate", "started", nil)
	fresh, err := c.refresh(ctx, a.Identity, a.Artifact)
	if err != nil {
		return err
	}
	a.Artifact = *fresh // Refresh expiring download URLs without changing the chosen bytes.
	Record(a, "gate", "succeeded", nil)
	stage = "download"
	Record(a, stage, "started", nil)
	payload := filepath.Join(WorkDir(a), "payload")
	if runtime.GOOS == "windows" {
		payload += ".exe"
	}
	if err := c.Download(ctx, a.Artifact, payload); err != nil {
		return err
	}
	Record(a, stage, "succeeded", nil)
	stage = "verify"
	Record(a, stage, "started", nil)
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	output, err := exec.CommandContext(verifyCtx, payload, "--version").Output()
	cancel()
	if err != nil || strings.TrimSpace(string(output)) != a.Artifact.TargetVersion {
		return fail("VERSION_MISMATCH", "下载程序的运行版本与目标不一致")
	}
	Record(a, stage, "succeeded", nil)
	stage = "prepare"
	Record(a, stage, "started", nil)
	if _, err := c.Key(); err != nil {
		return err
	}
	if err := c.Recheck(ctx, a.Identity, a.Artifact); err != nil {
		return err
	}
	if hash, err := fileHash(a.Target); err != nil || hash != a.OldHash {
		return fail("INSTALLATION_CHANGED", "安装前目标文件已改变")
	}
	if runtime.GOOS == "windows" {
		alias := filepath.Join(filepath.Dir(a.Target), "yc.exe")
		if alias != a.Target {
			if h, e := fileHash(alias); e != nil || h != a.OldHash {
				return fail("INSTALLATION_CHANGED", "Windows CLI 别名与主程序不一致")
			}
		}
	}
	state, err := h.Inspect()
	if err != nil {
		return err
	}
	if state.Busy {
		stage = "idle"
		Record(a, stage, "deferred", nil)
		return nil
	}
	if state.Running && (state.Executable != a.Target || state.Profile != a.Identity.Profile || "https://"+state.Host != a.Identity.BaseURL) {
		return fail("RUNTIME_CHANGED", "运行进程不属于此次安装")
	}
	// Only this CLI's runtime is stopped. Stop/Start preserve its existing supervision.
	Record(a, stage, "succeeded", nil)
	stage = "apply"
	Record(a, stage, "started", nil)
	if state.Running {
		if err := h.Stop(); err != nil {
			return err
		}
	}
	// Stage in the same directory: rename must not cross filesystems.
	staged := a.Target + ".new-" + a.UpdateID
	backup := a.Target + ".previous-" + a.UpdateID
	if err := copyFile(payload, staged); err != nil {
		if state.Running {
			_ = h.Start()
		}
		return err
	}
	defer os.Remove(staged)
	if err := os.Rename(a.Target, backup); err != nil {
		if state.Running {
			_ = h.Start()
		}
		return err
	}
	rollback := func(cause error) error {
		Record(a, "rollback", "started", nil)
		failed := a.Target + ".failed-" + a.UpdateID
		if _, e := os.Stat(a.Target); e == nil {
			if e = os.Rename(a.Target, failed); e != nil {
				Record(a, "rollback", "failed", e)
				return fmt.Errorf("%w; rollback could not move failed binary", cause)
			}
		}
		if e := os.Rename(backup, a.Target); e != nil {
			Record(a, "rollback", "failed", e)
			return fmt.Errorf("%w; rollback failed", cause)
		}
		if runtime.GOOS == "windows" {
			alias := filepath.Join(filepath.Dir(a.Target), "yc.exe")
			if alias != a.Target {
				aliasStage := alias + ".restore-" + a.UpdateID
				if e := copyFile(a.Target, aliasStage); e != nil {
					return e
				}
				if e := os.Rename(aliasStage, alias); e != nil {
					return e
				}
			}
		}
		if state.Running {
			if e := h.Start(); e != nil {
				Record(a, "rollback", "failed", e)
				return fmt.Errorf("%w; old daemon restart failed", cause)
			}
		}
		Record(a, "rollback", "succeeded", nil)
		return cause
	}
	if err := os.Rename(staged, a.Target); err != nil {
		return rollback(err)
	}
	if runtime.GOOS == "windows" {
		alias := filepath.Join(filepath.Dir(a.Target), "yc.exe")
		if alias != a.Target {
			aliasStage := alias + ".new-" + a.UpdateID
			if err := copyFile(payload, aliasStage); err != nil {
				return rollback(err)
			}
			defer os.Remove(aliasStage)
			// Windows permits replacement only after the prior daemon and parent command exit.
			if err := os.Rename(aliasStage, alias); err != nil {
				return rollback(err)
			}
		}
	}
	a.Ready = true
	if err := fsutil.WriteJSON(JobPath(a), a, 0600); err != nil {
		return rollback(err)
	}
	Record(a, stage, "succeeded", nil)
	e := EventFor(a, "activate", "started", nil)
	e.InstalledVersion = a.Artifact.TargetVersion
	_ = recordEvent(a.Identity, e)
	if state.Running {
		stage = "restart"
		Record(a, stage, "started", nil)
		if err := h.Start(); err != nil {
			return err
		} // Keep the installed version for diagnosis/retry.
		confirmed := false
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			current, err := h.Inspect()
			if err == nil && current.Running && current.Version == a.Artifact.TargetVersion && current.Executable == a.Target && current.Connected && "https://"+current.Host == a.Identity.BaseURL {
				Record(a, stage, "succeeded", nil)
				Activated(a)
				confirmed = true
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		if !confirmed {
			return fail("ACTIVATION_UNCONFIRMED", "已安装，但新版运行和连接未确认")
		}
	} else {
		Record(a, "restart", "skipped", nil)
	}
	// No stopped daemon is started solely to confirm an update. Its next startup confirms activation.
	if err := h.Skills(); err != nil {
		Record(a, "prepare", "failed", fail("SKILL_UPDATE_FAILED", "CLI 已安装，Claude skill 更新失败"))
	}
	_ = os.Remove(backup)
	_ = os.Remove(payload)
	return nil
}
func Activated(a Attempt) {
	// Only confirmed activation ends the installation cooldown.
	if err := os.Remove(filepath.Join(a.Identity.Directory(), "install-cooldown.json")); err != nil && !os.IsNotExist(err) {
		return
	}
	e := EventFor(a, "activate", "succeeded", nil)
	e.ReportID = a.UpdateID + "-activate"
	e.RunningVersion = a.Artifact.TargetVersion
	e.InstalledVersion = a.Artifact.TargetVersion
	if recordEvent(a.Identity, e) != nil {
		return
	}
	Final(a, nil)
	a.Ready = false
	_ = fsutil.WriteJSON(JobPath(a), a, 0600)
}
func ConfirmPending(id Identity, executable string) {
	dirs, _ := os.ReadDir(filepath.Join(id.Root, "update", "attempts"))
	for _, d := range dirs {
		if !d.IsDir() || !regexpID(d.Name()) {
			continue
		}
		path := filepath.Join(id.Root, "update", "attempts", d.Name(), "attempt.json")
		a, err := ReadAttempt(path)
		if err != nil || !a.Ready || a.Target != executable || a.Artifact.TargetVersion != id.CurrentVersion || a.Identity.Profile != id.Profile || a.Identity.KeyHash != id.KeyHash || a.Identity.BaseURL != id.BaseURL {
			continue
		}
		Activated(a)
	}
}

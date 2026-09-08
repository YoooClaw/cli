//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/diagnostics"
	"github.com/YoooClaw/cli/internal/fsutil"
	"time"
)

type platformManager struct{ root, id, unit, path string }

func newPlatformManager(root string) Manager {
	id := ServiceID(root)
	configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}
	unit := id + ".service"
	return &platformManager{root: root, id: id, unit: unit, path: filepath.Join(configHome, "systemd", "user", unit)}
}

var loginctl = func(args ...string) ([]byte, error) {
	return exec.Command("loginctl", append([]string{"--no-ask-password"}, args...)...).CombinedOutput()
}

func readLinger() (*bool, error) {
	out, err := loginctl("show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value")
	if err != nil {
		return nil, fmt.Errorf("无法查询 linger: %s (%v)", strings.TrimSpace(string(out)), err)
	}
	value := strings.TrimSpace(string(out))
	if value != "yes" && value != "no" {
		return nil, fmt.Errorf("无法识别 linger 状态: %q", value)
	}
	enabled := value == "yes"
	return &enabled, nil
}

func (m *platformManager) EnableBoot() (bootErr error) {
	defer func() {
		linger, err := readLinger()
		fields := map[string]any{"uid": os.Getuid(), "unit": m.unit, "linger": linger, "error": diagnostics.SafeError(bootErr), "queryError": diagnostics.SafeError(err)}
		level := "info"
		if bootErr != nil {
			level = "error"
		}
		diagnostics.Write(m.root, level, "autostart.boot", fields)
	}()
	if linger, err := readLinger(); err == nil && *linger {
		return nil
	}
	uid := strconv.Itoa(os.Getuid())
	if out, err := loginctl("enable-linger", uid); err != nil {
		return fmt.Errorf("无法启用开机自启: %s (%v)；请管理员执行 loginctl enable-linger %s", strings.TrimSpace(string(out)), err, uid)
	}
	linger, err := readLinger()
	if err != nil {
		return err
	}
	if !*linger {
		return fmt.Errorf("enable-linger 后仍未启用开机自启")
	}
	return nil
}

var systemctl = func(args ...string) ([]byte, error) {
	return exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
}

func (m *platformManager) Available() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("%w: systemctl 不可用", ErrUnavailable)
	}
	if out, err := systemctl("show-environment"); err != nil {
		return fmt.Errorf("%w: systemd user manager 不可用: %s", ErrUnavailable, strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Status() (Status, error) {
	status := Status{Manager: "systemd", Unit: m.unit}
	if _, err := os.Stat(m.path); err == nil {
		status.Installed = true
	}
	if status.Installed {
		enabledOut, _ := systemctl("is-enabled", m.unit)
		enabled := strings.TrimSpace(string(enabledOut)) == "enabled"
		status.UnitEnabled = &enabled
		linger, err := readLinger()
		status.Linger = linger
		switch {
		case err != nil:
			status.BootWarning = err.Error()
		case !*linger:
			status.BootWarning = "未启用 linger，仅配置用户登录自启；无人登录开机启动请运行 yoooclaw daemon autostart enable --boot"
		}
		if !enabled {
			status.BootWarning = "系统服务未 enable；请运行 yoooclaw daemon autostart enable"
		}
	}
	out, commandErr := systemctl("show", m.unit, "--property=LoadState,ActiveState,MainPID")
	loadState := ""
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "LoadState":
			loadState = value
			if value == "loaded" {
				status.Loaded = true
			}
		case "ActiveState":
			switch value {
			case "active", "activating", "reloading":
				status.Running = true
			}
		case "MainPID":
			if pid, err := strconv.Atoi(value); err == nil && pid > 0 {
				status.PID = pid
			}
		}
	}
	if commandErr != nil && loadState != "not-found" {
		// A missing unit is not an error for lifecycle operations. Ask systemd
		// for an exact unit listing instead of matching localized error text.
		listed, listErr := systemctl("list-units", "--all", "--full", "--plain", "--no-legend", m.unit)
		if listErr == nil && strings.TrimSpace(string(listed)) == "" {
			return status, nil
		}
		return status, fmt.Errorf("systemctl show 失败: %s (%v)", strings.TrimSpace(string(out)), commandErr)
	}
	return status, nil
}
func systemdQuote(value string) string {
	// systemd expands % specifiers even inside quotes; doubling prevents paths
	// supplied through YOOOCLAW_HOME from being interpreted as unit specifiers.
	return strconv.Quote(strings.ReplaceAll(value, "%", "%%"))
}
func unitText(spec Spec) string {
	args := []string{systemdQuote(spec.Executable)}
	for _, arg := range spec.Arguments {
		args = append(args, systemdQuote(arg))
	}
	return `[Unit]
Description=YoooClaw daemon
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
Environment=` + systemdQuote("YOOOCLAW_HOME="+spec.RootDir) + `
ExecStart=` + strings.Join(args, " ") + `
Restart=on-failure
RestartSec=5
StandardOutput=append:` + strings.ReplaceAll(spec.SupervisorLog, "%", "%%") + `
StandardError=append:` + strings.ReplaceAll(spec.SupervisorLog, "%", "%%") + `

[Install]
WantedBy=default.target
`
}
func (m *platformManager) command(args ...string) ([]byte, error) {
	began := time.Now()
	diagnostics.Write(m.root, "info", "systemd.command_begin", map[string]any{"args": args, "uid": os.Getuid(), "unit": m.unit})
	out, err := systemctl(args...)
	fields := map[string]any{"args": args, "uid": os.Getuid(), "unit": m.unit, "elapsedMs": time.Since(began).Milliseconds(), "success": err == nil}
	level := "info"
	if err != nil {
		level = "error"
		fields["error"] = diagnostics.SafeError(err)
		fields["output"] = diagnostics.SafeError(fmt.Errorf("%s", out))
	}
	diagnostics.Write(m.root, level, "systemd.command_end", fields)
	return out, err
}

func (m *platformManager) Install(spec Spec) error {
	diagnostics.Write(m.root, "info", "autostart.install", map[string]any{"unit": m.unit, "unitPath": m.path, "executable": spec.Executable, "root": spec.RootDir, "supervisorLog": spec.SupervisorLog})
	// Output paths are raw scalar values, not ExecStart word lists. Reject
	// line breaks instead of quoting them (quotes break the append: parser).
	if !filepath.IsAbs(spec.SupervisorLog) || strings.ContainsAny(spec.SupervisorLog, "\r\n\x00") || strings.TrimSpace(spec.SupervisorLog) != spec.SupervisorLog || strings.HasSuffix(spec.SupervisorLog, "\\") {
		return fmt.Errorf("无效的 supervisor 日志路径")
	}
	if err := fsutil.EnsureDir(filepath.Dir(spec.SupervisorLog), fsutil.DirMode); err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(m.path, []byte(unitText(spec)), fsutil.ConfigFileMode); err != nil {
		return err
	}
	if out, err := m.command("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %s", strings.TrimSpace(string(out)))
	}
	if out, err := m.command("enable", m.unit); err != nil {
		return fmt.Errorf("systemctl enable 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Start() error {
	out, err := m.command("start", m.unit)
	if err != nil {
		return fmt.Errorf("systemctl start 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Stop() error {
	status, err := m.Status()
	if err != nil {
		return err
	}
	if !status.Running {
		return nil
	}
	out, stopErr := m.command("stop", m.unit)
	after, statusErr := waitForServiceState(m.Status, func(status Status) bool { return !status.Running })
	if statusErr == nil && !after.Running {
		return nil
	}
	if stopErr != nil {
		return fmt.Errorf("systemctl stop 失败: %s", strings.TrimSpace(string(out)))
	}
	if statusErr != nil {
		return fmt.Errorf("systemctl stop 后无法确认服务状态: %w", statusErr)
	}
	return fmt.Errorf("systemctl stop 后服务仍在运行: %s", m.unit)
}
func (m *platformManager) Restart() error {
	out, err := m.command("restart", m.unit)
	if err != nil {
		return fmt.Errorf("systemctl restart 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Uninstall() error {
	if err := m.Stop(); err != nil {
		return err
	}
	status, err := m.Status()
	if err != nil {
		return err
	}
	if !status.Installed && !status.Loaded {
		return nil
	}
	if out, err := m.command("disable", m.unit); err != nil {
		return fmt.Errorf("systemctl disable 失败: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if out, err := m.command("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %s", strings.TrimSpace(string(out)))
	}
	after, err := m.Status()
	if err != nil {
		return err
	}
	if after.Installed || after.Loaded {
		return fmt.Errorf("systemd 服务卸载后仍有注册: %s", m.unit)
	}
	return nil
}

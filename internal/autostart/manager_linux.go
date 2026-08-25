//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/fsutil"
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
StandardOutput=` + systemdQuote("append:"+spec.SupervisorLog) + `
StandardError=` + systemdQuote("append:"+spec.SupervisorLog) + `

[Install]
WantedBy=default.target
`
}
func (m *platformManager) Install(spec Spec) error {
	if err := fsutil.EnsureDir(filepath.Dir(spec.SupervisorLog), fsutil.DirMode); err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(m.path, []byte(unitText(spec)), fsutil.ConfigFileMode); err != nil {
		return err
	}
	if out, err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %s", strings.TrimSpace(string(out)))
	}
	if out, err := systemctl("enable", m.unit); err != nil {
		return fmt.Errorf("systemctl enable 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Start() error {
	out, err := systemctl("start", m.unit)
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
	out, stopErr := systemctl("stop", m.unit)
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
	out, err := systemctl("restart", m.unit)
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
	if out, err := systemctl("disable", m.unit); err != nil {
		return fmt.Errorf("systemctl disable 失败: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if out, err := systemctl("daemon-reload"); err != nil {
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

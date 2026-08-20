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
func systemctl(args ...string) ([]byte, error) {
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
	out, err := systemctl("show", m.unit, "--property=LoadState,ActiveState,MainPID", "--value")
	if err != nil {
		return status, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, value := range lines {
		value = strings.TrimSpace(value)
		switch value {
		case "loaded":
			status.Loaded = true
		case "active", "activating", "reloading":
			status.Running = true
		default:
			if pid, e := strconv.Atoi(value); e == nil && pid > 0 {
				status.PID = pid
			}
		}
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
	out, err := systemctl("stop", m.unit)
	if err != nil && !strings.Contains(string(out), "not loaded") {
		return fmt.Errorf("systemctl stop 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Restart() error {
	out, err := systemctl("restart", m.unit)
	if err != nil {
		return fmt.Errorf("systemctl restart 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Uninstall() error {
	_, _ = systemctl("disable", m.unit)
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, _ = systemctl("daemon-reload")
	return nil
}

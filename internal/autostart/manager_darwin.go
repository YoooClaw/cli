//go:build darwin

package autostart

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/fsutil"
)

type platformManager struct {
	root  string
	id    string
	label string
	plist string
}

var launchctl = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

func newPlatformManager(root string) Manager {
	id := ServiceID(root)
	label := "com.yoooclaw." + strings.TrimPrefix(id, "yoooclaw-")
	home, _ := os.UserHomeDir()
	return &platformManager{root: root, id: id, label: label, plist: filepath.Join(home, "Library", "LaunchAgents", label+".plist")}
}

func (m *platformManager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func (m *platformManager) target() string { return m.domain() + "/" + m.label }
func (m *platformManager) Available() error {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return fmt.Errorf("%w: launchctl 不可用", ErrUnavailable)
	}
	return nil
}
func (m *platformManager) Status() (Status, error) {
	status := Status{Manager: "launchd", Unit: m.label}
	if _, err := os.Stat(m.plist); err == nil {
		status.Installed = true
	}
	out, err := launchctl("print", m.target())
	if err != nil {
		// Inspect the user domain instead of matching launchctl's localized
		// "service not found" text or relying on an OS-version-specific code.
		domainOut, domainErr := launchctl("print", m.domain())
		if domainErr == nil && !strings.Contains(string(domainOut), m.label) {
			return status, nil
		}
		return status, fmt.Errorf("launchctl print 失败: %s (%v)", strings.TrimSpace(string(out)), err)
	}
	status.Loaded = true
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			status.Running = strings.TrimSpace(strings.TrimPrefix(line, "state = ")) == "running"
		}
		if strings.HasPrefix(line, "pid = ") {
			status.PID, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid = ")))
		}
	}
	return status, nil
}
func plistXML(label string, spec Spec) string {
	args := append([]string{spec.Executable}, spec.Arguments...)
	var argXML strings.Builder
	for _, arg := range args {
		argXML.WriteString("    <string>" + html.EscapeString(arg) + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + html.EscapeString(label) + `</string>
  <key>ProgramArguments</key>
  <array>
` + argXML.String() + `  </array>
  <key>EnvironmentVariables</key>
  <dict><key>YOOOCLAW_HOME</key><string>` + html.EscapeString(spec.RootDir) + `</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Background</string>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>StandardOutPath</key><string>` + html.EscapeString(spec.SupervisorLog) + `</string>
  <key>StandardErrorPath</key><string>` + html.EscapeString(spec.SupervisorLog) + `</string>
</dict>
</plist>
`
}
func (m *platformManager) Install(spec Spec) error {
	if err := fsutil.EnsureDir(filepath.Dir(spec.SupervisorLog), fsutil.DirMode); err != nil {
		return err
	}
	return fsutil.WriteAtomic(m.plist, []byte(plistXML(m.label, spec)), fsutil.ConfigFileMode)
}
func (m *platformManager) Start() error {
	status, _ := m.Status()
	if !status.Loaded {
		if out, err := launchctl("bootstrap", m.domain(), m.plist); err != nil {
			return fmt.Errorf("launchctl bootstrap 失败: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	out, err := launchctl("kickstart", "-k", m.target())
	if err != nil {
		return fmt.Errorf("launchctl kickstart 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Stop() error {
	status, err := m.Status()
	if err != nil {
		return err
	}
	if !status.Loaded {
		return nil
	}
	out, stopErr := launchctl("bootout", m.target())
	after, statusErr := waitForServiceState(m.Status, func(status Status) bool { return !status.Loaded })
	if statusErr == nil && !after.Loaded {
		return nil
	}
	if stopErr != nil {
		return fmt.Errorf("launchctl bootout 失败: %s", strings.TrimSpace(string(out)))
	}
	if statusErr != nil {
		return fmt.Errorf("launchctl bootout 后无法确认服务状态: %w", statusErr)
	}
	return fmt.Errorf("launchctl bootout 后服务仍处于加载状态: %s", m.target())
}
func (m *platformManager) Restart() error {
	if err := m.Stop(); err != nil {
		return err
	}
	return m.Start()
}
func (m *platformManager) Uninstall() error {
	if err := m.Stop(); err != nil {
		return err
	}
	if err := os.Remove(m.plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

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
	out, err := exec.Command("launchctl", "print", m.target()).CombinedOutput()
	if err != nil {
		return status, nil
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
		if out, err := exec.Command("launchctl", "bootstrap", m.domain(), m.plist).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap 失败: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	out, err := exec.Command("launchctl", "kickstart", "-k", m.target()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Stop() error {
	status, _ := m.Status()
	if !status.Loaded {
		return nil
	}
	out, err := exec.Command("launchctl", "bootout", m.target()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootout 失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
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

//go:build windows

package autostart

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

type platformManager struct{ root, id, task string }

func newPlatformManager(root string) Manager {
	id := ServiceID(root)
	return &platformManager{root: root, id: id, task: `\YoooClaw\` + id}
}
func (m *platformManager) Available() error {
	if _, err := exec.LookPath("schtasks.exe"); err != nil {
		return fmt.Errorf("%w: schtasks 不可用", ErrUnavailable)
	}
	return nil
}
func schtasks(args ...string) ([]byte, error) {
	return exec.Command("schtasks.exe", args...).CombinedOutput()
}
func (m *platformManager) Status() (Status, error) {
	status := Status{Manager: "task-scheduler", Unit: m.task}
	out, err := schtasks("/Query", "/TN", m.task, "/FO", "LIST", "/V")
	if err != nil {
		return status, nil
	}
	status.Installed, status.Loaded = true, true
	text := strings.ToLower(string(out))
	status.Running = strings.Contains(text, "status:") && strings.Contains(text, "running")
	return status, nil
}
func taskXML(spec Spec, userSID string) string {
	args := make([]string, 0, len(spec.Arguments)+2)
	for _, arg := range append(spec.Arguments, "--format", "json") {
		args = append(args, syscall.EscapeArg(arg))
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + html.EscapeString(userSID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><RestartOnFailure><Interval>PT1M</Interval><Count>5</Count></RestartOnFailure><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Enabled>true</Enabled></Settings>
  <Actions Context="Author"><Exec><Command>` + html.EscapeString(spec.Executable) + `</Command><Arguments>` + html.EscapeString(strings.Join(args, " ")) + `</Arguments><WorkingDirectory>` + html.EscapeString(spec.RootDir) + `</WorkingDirectory></Exec></Actions>
</Task>`
}

func currentUserSID() (string, error) {
	out, err := exec.Command("whoami.exe", "/user", "/fo", "csv", "/nh").Output()
	if err != nil {
		return "", fmt.Errorf("查询当前用户 SID 失败: %w", err)
	}
	records, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil {
		return "", fmt.Errorf("解析当前用户 SID 失败: %w", err)
	}
	for _, record := range records {
		for _, field := range record {
			field = strings.TrimSpace(field)
			if strings.HasPrefix(field, "S-1-") {
				return field, nil
			}
		}
	}
	return "", fmt.Errorf("whoami 未返回当前用户 SID")
}

func (m *platformManager) Install(spec Spec) error {
	userSID, err := currentUserSID()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "yoooclaw-task-*.xml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	content := []byte("\xff\xfe" + utf16LE(taskXML(spec, userSID)))
	if _, err = tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	out, err := schtasks("/Create", "/TN", m.task, "/XML", name, "/F")
	if err != nil {
		return fmt.Errorf("创建计划任务失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func utf16LE(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r <= 0xffff {
			b.WriteByte(byte(r))
			b.WriteByte(byte(r >> 8))
		} else {
			r -= 0x10000
			hi, lo := 0xd800+(r>>10), 0xdc00+(r&0x3ff)
			b.WriteByte(byte(hi))
			b.WriteByte(byte(hi >> 8))
			b.WriteByte(byte(lo))
			b.WriteByte(byte(lo >> 8))
		}
	}
	return b.String()
}
func (m *platformManager) Start() error {
	out, err := schtasks("/Run", "/TN", m.task)
	if err != nil {
		return fmt.Errorf("启动计划任务失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *platformManager) Stop() error {
	status, _ := m.Status()
	if !status.Installed {
		return nil
	}
	// /End is idempotent for our purposes. Its "not currently running" error
	// text is localized, so do not parse it; the daemon lock/HTTP stop path
	// still verifies and retires any process that remains alive.
	_, _ = schtasks("/End", "/TN", m.task)
	return nil
}
func (m *platformManager) Restart() error {
	if err := m.Stop(); err != nil {
		return err
	}
	return m.Start()
}
func (m *platformManager) Uninstall() error {
	status, _ := m.Status()
	if !status.Installed {
		return nil
	}
	out, err := schtasks("/Delete", "/TN", m.task, "/F")
	if err != nil {
		return fmt.Errorf("删除计划任务失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

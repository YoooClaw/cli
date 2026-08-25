//go:build windows

package autostart

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
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

var schtasks = func(args ...string) ([]byte, error) {
	return exec.Command("schtasks.exe", args...).CombinedOutput()
}

func (m *platformManager) taskFile() string {
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	relative := strings.ReplaceAll(strings.TrimPrefix(m.task, `\`), `\`, "/")
	return filepath.Join(systemRoot, "System32", "Tasks", filepath.FromSlash(relative))
}
func (m *platformManager) Status() (Status, error) {
	status := Status{Manager: "task-scheduler", Unit: m.task}
	out, err := schtasks("/Query", "/TN", m.task, "/FO", "LIST", "/V")
	if err != nil {
		if _, statErr := os.Stat(m.taskFile()); os.IsNotExist(statErr) {
			return status, nil
		}
		return status, fmt.Errorf("查询计划任务失败: %s", strings.TrimSpace(string(out)))
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
	status, err := m.Status()
	if err != nil {
		return err
	}
	if !status.Installed || !status.Running {
		return nil
	}
	out, stopErr := schtasks("/End", "/TN", m.task)
	after, statusErr := waitForServiceState(m.Status, func(status Status) bool {
		return !status.Installed || !status.Running
	})
	if statusErr == nil && (!after.Installed || !after.Running) {
		return nil
	}
	if stopErr != nil {
		return fmt.Errorf("停止计划任务失败: %s", strings.TrimSpace(string(out)))
	}
	if statusErr != nil {
		return fmt.Errorf("停止计划任务后无法确认状态: %w", statusErr)
	}
	return fmt.Errorf("停止计划任务后任务仍在运行: %s", m.task)
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
	status, err := m.Status()
	if err != nil {
		return err
	}
	if !status.Installed {
		return nil
	}
	out, deleteErr := schtasks("/Delete", "/TN", m.task, "/F")
	after, statusErr := m.Status()
	if statusErr == nil && !after.Installed {
		return nil
	}
	if deleteErr != nil {
		return fmt.Errorf("删除计划任务失败: %s", strings.TrimSpace(string(out)))
	}
	if statusErr != nil {
		return fmt.Errorf("删除计划任务后无法确认状态: %w", statusErr)
	}
	return fmt.Errorf("删除计划任务后任务仍存在: %s", m.task)
}

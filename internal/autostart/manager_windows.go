//go:build windows

package autostart

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type platformManager struct{ root, id, task string }

// Task Scheduler accepts Stop synchronously but can keep reporting Running
// while it tears down the task instance. Three seconds was too short on some
// Windows endpoints with endpoint-security hooks installed.
var windowsTaskStopTimeout = 15 * time.Second

func newPlatformManager(root string) Manager {
	id := ServiceID(root)
	return &platformManager{root: root, id: id, task: `\YoooClaw\` + id}
}
func (m *platformManager) Available() error {
	out, err := taskSchedulerCOM("available")
	if err != nil {
		return fmt.Errorf("%w: Windows Task Scheduler COM 不可用: %s", ErrUnavailable, commandError(out, err))
	}
	return nil
}

var taskSchedulerCOM = func(action string, args ...string) ([]byte, error) {
	script, ok := taskSchedulerScripts[action]
	if !ok {
		return nil, fmt.Errorf("未知 Task Scheduler COM 操作: %s", action)
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "YOOOCLAW_TASK_SCHEDULER_ARGS="+string(payload))
	return cmd.CombinedOutput()
}

var taskSchedulerScripts = map[string]string{
	"available": `$ErrorActionPreference='Stop'; $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $null=$s.GetFolder('\'); 'ok'`,
	"identity":  `$ErrorActionPreference='Stop'; [Security.Principal.WindowsIdentity]::GetCurrent().User.Value`,
	"status":    `$ErrorActionPreference='Stop'; $a=@(ConvertFrom-Json $env:YOOOCLAW_TASK_SCHEDULER_ARGS); $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); try { $f=$s.GetFolder($a[0]); $t=$f.GetTask($a[1]) } catch { 'missing'; exit 0 }; [int]$t.State`,
	"install":   `$ErrorActionPreference='Stop'; $a=@(ConvertFrom-Json $env:YOOOCLAW_TASK_SCHEDULER_ARGS); $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); try { $f=$s.GetFolder($a[0]) } catch { $f=$s.GetFolder('\').CreateFolder($a[0].Trim('\')) }; $xml=[IO.File]::ReadAllText($a[2],[Text.Encoding]::Unicode); $sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value; $null=$f.RegisterTask($a[1],$xml,6,$sid,$null,3,$null); 'ok'`,
	"start":     `$ErrorActionPreference='Stop'; $a=@(ConvertFrom-Json $env:YOOOCLAW_TASK_SCHEDULER_ARGS); $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $t=$s.GetFolder($a[0]).GetTask($a[1]); $null=$t.Run($null); 'ok'`,
	"stop":      `$ErrorActionPreference='Stop'; $a=@(ConvertFrom-Json $env:YOOOCLAW_TASK_SCHEDULER_ARGS); $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $t=$s.GetFolder($a[0]).GetTask($a[1]); $t.Stop(0); 'ok'`,
	"delete":    `$ErrorActionPreference='Stop'; $a=@(ConvertFrom-Json $env:YOOOCLAW_TASK_SCHEDULER_ARGS); $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $s.GetFolder($a[0]).DeleteTask($a[1],0); 'ok'`,
}

const windowsTaskStateRunning = 4

func (m *platformManager) folderAndName() (string, string) {
	trimmed := strings.Trim(m.task, `\`)
	name := trimmed
	taskPath := `\`
	if split := strings.LastIndex(trimmed, `\`); split >= 0 {
		taskPath = `\` + trimmed[:split]
		name = trimmed[split+1:]
	}
	return taskPath, name
}

func windowsSystemRoot() string {
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		return `C:\Windows`
	}
	return systemRoot
}
func (m *platformManager) Status() (Status, error) {
	status := Status{Manager: "task-scheduler", Unit: m.task}
	folder, name := m.folderAndName()
	out, err := taskSchedulerCOM("status", folder, name)
	if err != nil {
		return status, fmt.Errorf("查询计划任务失败: %s", commandError(out, err))
	}
	value := strings.TrimSpace(string(out))
	if value == "missing" {
		return status, nil
	}
	state, err := strconv.Atoi(value)
	if err != nil {
		return status, fmt.Errorf("解析计划任务状态失败: %q", value)
	}
	status.Installed, status.Loaded = true, true
	status.Running = state == windowsTaskStateRunning
	return status, nil
}
func taskXML(spec Spec, userSID string) string {
	runtimeArgs := make([]string, 0, len(spec.Arguments)+2)
	for _, arg := range append(spec.Arguments, "--format", "json") {
		runtimeArgs = append(runtimeArgs, powershellSingleQuote(arg))
	}
	command := "& " + powershellSingleQuote(spec.Executable) + " " + strings.Join(runtimeArgs, " ")
	powershell := filepath.Join(windowsSystemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	args := "-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -Command " + syscall.EscapeArg(command)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + html.EscapeString(userSID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><RestartOnFailure><Interval>PT1M</Interval><Count>5</Count></RestartOnFailure><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Enabled>true</Enabled><Hidden>true</Hidden></Settings>
  <Actions Context="Author"><Exec><Command>` + html.EscapeString(powershell) + `</Command><Arguments>` + html.EscapeString(args) + `</Arguments><WorkingDirectory>` + html.EscapeString(spec.RootDir) + `</WorkingDirectory></Exec></Actions>
</Task>`
}

func powershellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func currentUserSID() (string, error) {
	out, err := taskSchedulerCOM("identity")
	if err != nil {
		return "", fmt.Errorf("查询当前用户 SID 失败: %s", commandError(out, err))
	}
	sid := strings.TrimSpace(string(out))
	if !strings.HasPrefix(sid, "S-1-") {
		return "", fmt.Errorf("WindowsIdentity 未返回当前用户 SID: %q", sid)
	}
	return sid, nil
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
	folder, taskName := m.folderAndName()
	out, err := taskSchedulerCOM("install", folder, taskName, name)
	if err != nil {
		return fmt.Errorf("创建计划任务失败: %s", commandError(out, err))
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
	folder, name := m.folderAndName()
	out, err := taskSchedulerCOM("start", folder, name)
	if err != nil {
		return fmt.Errorf("启动计划任务失败: %s", commandError(out, err))
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
	folder, name := m.folderAndName()
	out, stopErr := taskSchedulerCOM("stop", folder, name)
	after, statusErr := waitForServiceStateWithin(m.Status, func(status Status) bool {
		return !status.Installed || !status.Running
	}, windowsTaskStopTimeout)
	if statusErr == nil && (!after.Installed || !after.Running) {
		return nil
	}
	if stopErr != nil {
		return fmt.Errorf("停止计划任务失败: %s", commandError(out, stopErr))
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
	folder, name := m.folderAndName()
	out, deleteErr := taskSchedulerCOM("delete", folder, name)
	after, statusErr := m.Status()
	if statusErr == nil && !after.Installed {
		return nil
	}
	if deleteErr != nil {
		return fmt.Errorf("删除计划任务失败: %s", commandError(out, deleteErr))
	}
	if statusErr != nil {
		return fmt.Errorf("删除计划任务后无法确认状态: %w", statusErr)
	}
	return fmt.Errorf("删除计划任务后任务仍存在: %s", m.task)
}

func commandError(out []byte, err error) string {
	message := strings.TrimSpace(string(out))
	if message != "" {
		return message
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

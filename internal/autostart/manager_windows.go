//go:build windows

package autostart

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
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

// Query and manage tasks in-process; no PowerShell helper is launched.
// Existing task identity and VBS hidden-launch behavior remain unchanged.
var taskSchedulerCOM = nativeTaskSchedulerCOM

const (
	windowsTaskStateRunning = 4
)

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

func (m *platformManager) launcherPath() string {
	return filepath.Join(m.root, "yoooclaw-daemon-hidden.vbs")
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
func taskXML(spec Spec, userSID, launcher string) string {
	wscript := filepath.Join(windowsSystemRoot(), "System32", "wscript.exe")
	args := "//B //NoLogo " + syscall.EscapeArg(launcher)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><UserId>` + html.EscapeString(userSID) + `</UserId><Enabled>true</Enabled></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + html.EscapeString(userSID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><StartWhenAvailable>true</StartWhenAvailable><RestartOnFailure><Interval>PT1M</Interval><Count>5</Count></RestartOnFailure><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Enabled>true</Enabled><Hidden>true</Hidden></Settings>
  <Actions Context="Author"><Exec><Command>` + html.EscapeString(wscript) + `</Command><Arguments>` + html.EscapeString(args) + `</Arguments><WorkingDirectory>` + html.EscapeString(spec.RootDir) + `</WorkingDirectory></Exec></Actions>
</Task>`
}

func vbscriptString(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func hiddenLauncherVBS(spec Spec) string {
	commandArgs := make([]string, 0, len(spec.Arguments)+3)
	commandArgs = append(commandArgs, syscall.EscapeArg(spec.Executable))
	for _, arg := range append(spec.Arguments, "--format", "json") {
		commandArgs = append(commandArgs, syscall.EscapeArg(arg))
	}
	command := strings.Join(commandArgs, " ")
	return "Option Explicit\r\n" +
		"Dim shell, exitCode\r\n" +
		"Set shell = CreateObject(\"WScript.Shell\")\r\n" +
		"exitCode = shell.Run(" + vbscriptString(command) + ", 0, True)\r\n" +
		"WScript.Quit exitCode\r\n"
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
	launcher := m.launcherPath()
	if err := fsutil.WriteAtomic(launcher, []byte(hiddenLauncherVBS(spec)), fsutil.ConfigFileMode); err != nil {
		return fmt.Errorf("写入 Windows daemon 隐藏启动器失败: %w", err)
	}
	folder, taskName := m.folderAndName()
	// Pass Unicode XML directly through COM instead of a PowerShell temp file.
	out, err := taskSchedulerCOM("install", folder, taskName, taskXML(spec, userSID, launcher), userSID)
	if err != nil {
		return fmt.Errorf("创建计划任务失败: %s", commandError(out, err))
	}
	return nil
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
	if status.Installed {
		folder, name := m.folderAndName()
		out, deleteErr := taskSchedulerCOM("delete", folder, name)
		after, statusErr := m.Status()
		if statusErr != nil {
			return fmt.Errorf("删除计划任务后无法确认状态: %w", statusErr)
		}
		if after.Installed {
			if deleteErr != nil {
				return fmt.Errorf("删除计划任务失败: %s", commandError(out, deleteErr))
			}
			return fmt.Errorf("删除计划任务后任务仍存在: %s", m.task)
		}
	}
	if err := os.Remove(m.launcherPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 Windows daemon 隐藏启动器失败: %w", err)
	}
	return nil
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

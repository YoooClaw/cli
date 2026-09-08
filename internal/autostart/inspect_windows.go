//go:build windows

package autostart

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/winhost"
	"github.com/YoooClaw/cli/internal/winproc"
)

func (m *platformManager) definition(spec Spec) (taskDefinition, string, error) {
	folder, name := m.folderAndName()
	raw, err := taskSchedulerCOM("xml", folder, name)
	if err != nil {
		return taskDefinition{}, "", err
	}
	d, err := parseTaskDefinition(string(raw))
	if err != nil {
		return d, "", err
	}
	sid, err := currentUserSID()
	if err != nil {
		return d, "", err
	}
	host, err := winhost.Path(m.root)
	if err != nil {
		return d, "", err
	}
	if !d.Settings.Enabled || len(d.Principals) != 1 || d.Principals[0].UserID != sid || d.Principals[0].LogonType != "InteractiveToken" || d.Principals[0].RunLevel != "LeastPrivilege" {
		return d, "", fmt.Errorf("%w: 任务必须为当前用户、交互登录、最低权限且已启用", errTaskDefinitionMismatch)
	}
	if len(d.Actions.Exec) != 1 || strings.Contains(d.Actions.Inner, "ComHandler") || strings.Contains(d.Actions.Inner, "SendEmail") || strings.Contains(d.Actions.Inner, "ShowMessage") {
		return d, "", fmt.Errorf("%w: 任务应只有一个原生 host 动作", errTaskDefinitionMismatch)
	}
	a := d.Actions.Exec[0]
	if !strings.EqualFold(filepath.Clean(a.Command), filepath.Clean(host)) || a.Arguments != hostArguments(spec) || !strings.EqualFold(filepath.Clean(a.WorkingDirectory), filepath.Clean(spec.RootDir)) {
		return d, "", fmt.Errorf("%w: 任务动作与当前 CLI/root/原生 host 不一致", errTaskDefinitionMismatch)
	}
	if _, err := os.Stat(host); err != nil {
		if os.IsNotExist(err) {
			return d, "", fmt.Errorf("%w: 原生 host 不存在", errTaskDefinitionMismatch)
		}
		return d, "", err
	}
	login := false
	for _, t := range d.Triggers.Logon {
		if t.Enabled && t.UserID == sid {
			login = true
		}
	}
	if !login {
		return d, "", fmt.Errorf("%w: 任务缺少当前用户登录触发器", errTaskDefinitionMismatch)
	}
	return d, string(raw), nil
}

func (m *platformManager) Inspect(spec Spec, daemonPID int) (Inspection, error) {
	result := Inspection{}
	if _, _, err := m.definition(spec); err != nil {
		result.Reason = err.Error()
		if errors.Is(err, errTaskDefinitionMismatch) {
			return result, nil
		}
		return result, err
	}
	result.DefinitionMatches = true
	if daemonPID <= 0 {
		result.Reason = "daemon 未运行"
		return result, nil
	}
	var marker struct{ Host, Child winproc.Identity }
	b, err := os.ReadFile(filepath.Join(m.root, "daemon-host.json"))
	if err == nil {
		err = json.Unmarshal(b, &marker)
	}
	if err != nil {
		result.Reason = "无法读取原生 host 运行证据: " + err.Error()
		return result, nil
	}
	if marker.Child.PID != uint32(daemonPID) {
		result.Reason = "host 记录的 daemon PID 不匹配"
		return result, nil
	}
	host, err := winproc.Inspect(marker.Host.PID)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	child, err := winproc.Inspect(marker.Child.PID)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	expectedHost, _ := winhost.Path(m.root)
	if !winproc.Same(host, marker.Host) || !winproc.Same(child, marker.Child) || child.ParentPID != host.PID || child.Created < host.Created || !strings.EqualFold(child.Executable, spec.Executable) || !strings.EqualFold(host.Executable, expectedHost) {
		result.Reason = "进程路径、父子关系或创建时间不匹配"
		return result, nil
	}
	folder, name := m.folderAndName()
	pids, err := taskSchedulerCOM("instances", folder, name)
	if err != nil {
		result.Reason = err.Error()
		return result, err
	}
	for _, p := range strings.Split(string(pids), ",") {
		pid, err := strconv.ParseUint(p, 10, 32)
		if err != nil || pid == 0 {
			continue
		}
		ancestor := host
		for i := 0; i < 8; i++ {
			if ancestor.PID == uint32(pid) {
				result.ManagedDaemonVerified = true
				result.DaemonPID = daemonPID
				return result, nil
			}
			parent, err := winproc.Inspect(ancestor.ParentPID)
			if err != nil || parent.Created > ancestor.Created {
				break
			}
			ancestor = parent
		}
	}
	result.Reason = "无法关联当前计划任务实例与原生 host；不能宣称已验证系统托管"
	return result, nil
}

func (m *platformManager) Schedule(spec Spec, delay time.Duration) (time.Time, error) {
	if delay < 10*time.Second || delay > 10*time.Minute {
		return time.Time{}, fmt.Errorf("delay 必须在 10s 到 10m 之间")
	}
	status, err := m.Status()
	if err != nil {
		return time.Time{}, err
	}
	if !status.Installed || status.Running {
		return time.Time{}, fmt.Errorf("仅允许为已注册且未运行的任务安排启动")
	}
	_, raw, err := m.definition(spec)
	if err != nil {
		return time.Time{}, err
	}
	at := time.Now().Add(delay).Truncate(time.Second)
	updated, err := withDeferredTrigger(raw, at)
	if err != nil {
		return time.Time{}, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return time.Time{}, err
	}
	folder, name := m.folderAndName()
	state, err := taskSchedulerCOM("status", folder, name)
	if err != nil {
		return time.Time{}, err
	}
	if strings.TrimSpace(string(state)) != "3" {
		return time.Time{}, fmt.Errorf("任务不是 Ready 状态，不能登记延迟启动")
	}
	if _, err := taskSchedulerCOM("update", folder, name, updated, sid); err != nil {
		return time.Time{}, err
	}
	d, _, err := m.definition(spec)
	if err != nil {
		return time.Time{}, err
	}
	count := 0
	for _, trigger := range d.Triggers.Time {
		if trigger.ID != deferredTriggerID {
			continue
		}
		got, err := time.Parse(time.RFC3339, trigger.StartBoundary)
		if err != nil || !got.Equal(at) || !trigger.Enabled {
			return time.Time{}, fmt.Errorf("延迟触发器回读校验失败")
		}
		count++
	}
	if count != 1 || !at.After(time.Now()) {
		return time.Time{}, fmt.Errorf("触发器数量异常或启动时间已过；连接待验证")
	}
	return at, nil
}

// Package taskrepair contains the conservative validation rules shared by the
// Windows one-shot repair tool and its platform-independent tests.
package taskrepair

import (
	"encoding/xml"
	"fmt"
	"html"
	"strings"
)

const TaskPath = `\YoooClaw\yoooclaw-daemon`

type Task struct {
	Principals []struct {
		UserID    string `xml:"UserId"`
		LogonType string
		RunLevel  string
	} `xml:"Principals>Principal"`
	Actions struct {
		Exec  []struct{ Command, Arguments, WorkingDirectory string }
		Other []struct{ XMLName xml.Name } `xml:",any"`
	}
}

func same(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, `\`), strings.TrimRight(b, `\`))
}

// Validate refuses unknown task layouts instead of turning this tool into a
// general privileged task editor. resolve converts account names to SIDs.
func Validate(raw, sid, profile string, resolve func(string) (string, error)) error {
	var task Task
	// COM returns a Go UTF-8 string even though the XML declaration says UTF-16.
	raw = strings.Replace(raw, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if err := xml.Unmarshal([]byte(raw), &task); err != nil {
		return err
	}
	if len(task.Principals) != 1 {
		return fmt.Errorf("任务运行身份数量异常")
	}
	p := task.Principals[0]
	actual, err := resolve(p.UserID)
	if err != nil || actual != sid {
		return fmt.Errorf("任务不属于当前用户，停止修复")
	}
	if p.LogonType != "InteractiveToken" || (p.RunLevel != "" && p.RunLevel != "LeastPrivilege") {
		return fmt.Errorf("任务不是当前用户普通权限登录任务，停止修复")
	}
	if len(task.Actions.Exec) != 1 || len(task.Actions.Other) != 0 {
		return fmt.Errorf("任务动作异常，停止修复")
	}
	a := task.Actions.Exec[0]
	root := profile + `\.yoooclaw`
	bin := profile + `\AppData\Local\YoooClaw\bin`
	legacy := strings.HasPrefix(strings.ToLower(a.Command), strings.ToLower(profile+`\.workbuddy\binaries\node\versions\`)) && strings.HasSuffix(strings.ToLower(a.Command), `\node_modules\@yoooclaw\cli-win32-x64\bin\yc.exe`)
	// Reject traversal even when it superficially matches a legacy prefix.
	if strings.Contains(a.Command, `\..\`) || strings.Contains(a.Command, `/`) {
		return fmt.Errorf("任务路径异常")
	}
	native := same(a.Command, bin+`\yoooclaw.exe`)
	host := same(a.Command, bin+`\yoooclaw-repair-host.exe`)
	if !legacy && !native && !host {
		return fmt.Errorf("不是支持的旧 npm / 原生 CLI 任务，不修改未知任务")
	}
	args := `daemon run-service --root ` + root + ` --format json`
	quoted := `daemon run-service --root "` + root + `" --format json`
	if host {
		args, quoted = "--host", "--host"
	}
	if !same(a.WorkingDirectory, root) || (a.Arguments != args && a.Arguments != quoted) {
		return fmt.Errorf("任务参数或数据目录不符合预期，停止修复")
	}
	return nil
}

func XML(sid, root, host string) string {
	e := html.EscapeString
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
 <Triggers><LogonTrigger><UserId>` + e(sid) + `</UserId><Enabled>true</Enabled></LogonTrigger></Triggers>
 <Principals><Principal id="Author"><UserId>` + e(sid) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
 <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><StartWhenAvailable>true</StartWhenAvailable><RestartOnFailure><Interval>PT1M</Interval><Count>5</Count></RestartOnFailure><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Enabled>true</Enabled><Hidden>true</Hidden></Settings>
 <Actions Context="Author"><Exec><Command>` + e(host) + `</Command><Arguments>--host</Arguments><WorkingDirectory>` + e(root) + `</WorkingDirectory></Exec></Actions>
</Task>`
}

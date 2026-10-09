//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/YoooClaw/cli/internal/autostart"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/diagnostics"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/taskrepair"
	"github.com/YoooClaw/cli/internal/wintask"
)

const repairVersion = "native-v2-20260923"

var errNoRepair = errors.New("无需执行旧任务迁移")

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--host" {
		os.Exit(host())
	}
	if len(os.Args) == 3 && os.Args[1] == "--repair-acl" {
		if err := wintask.GrantTask(os.Args[2]); err != nil {
			message("未完成权限修复：\n"+diagnostics.SafeError(err), 0x10)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		message("不支持的参数", 0x10)
		os.Exit(1)
	}
	report, err := repair()
	if errors.Is(err, errNoRepair) {
		message(err.Error()+"\n未修改任务或权限。\n\n检查报告：\n"+report, 0x40)
		return
	}
	if err != nil {
		message("修复未完成：\n"+diagnostics.SafeError(err)+"\n\n请发送此目录中的日志供排查：\n"+report+"\n请勿反复运行或删除数据。", 0x10)
		os.Exit(1)
	}
	message("修复成功：系统任务已启动，Relay 已连接，间隔 60 秒复查通过。\n\n实际注销/重新登录尚未测试。无需保持本窗口开启。\n报告及备份：\n"+report, 0x40)
}

// This same GUI-subsystem binary is the persistent, no-console task launcher.
// It runs ONLY the CLI next to it, as the normal task user, never elevated.
func host() int {
	if windows.GetCurrentProcessToken().IsElevated() {
		return 1
	}
	sid, err := wintask.CurrentSID()
	if err != nil {
		return 1
	}
	profile, err := wintask.ProfileFor(sid)
	if err != nil {
		return 1
	}
	bin := filepath.Join(profile, `AppData\Local\YoooClaw\bin`)
	self, err := os.Executable()
	if err != nil || !strings.EqualFold(self, filepath.Join(bin, "yoooclaw-repair-host.exe")) {
		return 1
	}
	cmd := exec.Command(filepath.Join(bin, "yoooclaw.exe"), "daemon", "run-service", "--root", filepath.Join(profile, ".yoooclaw"), "--format", "json")
	cmd.Dir = filepath.Join(profile, ".yoooclaw")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err = cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		return 1
	}
	return 0
}

func repair() (report string, err error) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return "", fmt.Errorf("请普通双击启动，不要右键以管理员运行；工具会单独请求必要的授权")
	}
	if os.Getenv("YOOOCLAW_HOME") != "" {
		return "", fmt.Errorf("检测到自定义数据目录，此工具仅处理默认安装")
	}
	sid, err := wintask.CurrentSID()
	if err != nil {
		return "", err
	}
	profile, err := wintask.ProfileFor(sid)
	if err != nil {
		return "", err
	}
	root := filepath.Join(profile, ".yoooclaw")
	bin := filepath.Join(profile, `AppData\Local\YoooClaw\bin`)
	exe := filepath.Join(bin, "yoooclaw.exe")
	if info, e := os.Stat(exe); e != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("未找到原生 CLI：%s", exe)
	}
	if !strings.EqualFold(paths.RootDir(), root) {
		return "", fmt.Errorf("实际数据目录与用户身份不一致")
	}
	if active := paths.ReadActiveProfile(); active != "" && active != "default" {
		return "", fmt.Errorf("当前不是 default profile，需要人工核实")
	}
	p := paths.ForRoot(root, "default")
	cfg, err := config.Load(p)
	if err != nil {
		return "", fmt.Errorf("读取配置失败，不会重新初始化：%w", err)
	}
	if cfg.Ingress.Mode != "standalone" || !cfg.Relay.Enabled {
		return "", fmt.Errorf("当前不是 standalone + Relay 配置，不自动修改 owner 或环境")
	}
	documents, e := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
	if e != nil {
		return "", e
	}
	report, err = os.MkdirTemp(documents, "YoooClaw-Repair-")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(report, "repair.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return report, err
	}
	defer f.Close()
	l := log.New(f, "", log.LstdFlags)
	defer func() {
		if err != nil {
			if errors.Is(err, errNoRepair) {
				l.Printf("NO_CHANGE: %s", err)
			} else {
				l.Printf("FAILED: %s", diagnostics.SafeError(err))
			}
		}
	}()
	l.Printf("Build=%s Target SID=%s CLI=%s task=%s; no keys or full config logged", repairVersion, sid, exe, taskrepair.TaskPath)
	s, err := wintask.Open(l.Printf)
	if err != nil {
		if taskrepair.MissingTask(err) {
			l.Print("NO_CHANGE: no legacy task/folder exists; no repair needed for this migration case")
			return report, fmt.Errorf("%w：本机未找到旧 YoooClaw 任务；这不是连接成功的证明", errNoRepair)
		}
		return report, err
	}
	defer s.Close()
	if daemon.State(p).Running {
		code, body, e := daemon.NewClient(p).WithTimeout(3*time.Second).Request("GET", "/daemon/status", nil)
		m, _ := body.(map[string]any)
		relay, _ := m["relay"].(map[string]any)
		running, re := s.Running()
		enabled, ee := s.Enabled()
		if e == nil && re == nil && ee == nil && running && enabled && code == 200 && m["ingressMode"] == "standalone" && relay["mode"] == "relay" && relay["connected"] == true {
			l.Print("NO_CHANGE: task running and enabled, standalone daemon and Relay online; task migration/ACL not modified")
			return report, fmt.Errorf("%w：当前任务运行、daemon 与 Relay 在线，不打断正常服务；未测试注销登录", errNoRepair)
		}
		return report, fmt.Errorf("daemon 正在运行但未确认任务和 Relay 均正常；此工具不打断现有进程，需另行诊断")
	}
	raw, err := s.XML()
	if err != nil {
		return report, err
	}
	if err = taskrepair.Validate(raw, sid, profile, wintask.ResolveSID); err != nil {
		return report, err
	}
	running, err := s.Running()
	if err != nil {
		return report, err
	}
	if running {
		return report, fmt.Errorf("现有任务正在运行，停止修改以免打断服务")
	}
	before, err := s.SD()
	if err != nil {
		return report, fmt.Errorf("无法备份任务安全描述符，停止修改：%w", err)
	}
	for name, value := range map[string]string{"task-before.xml": strings.Replace(raw, `encoding="UTF-16"`, `encoding="UTF-8"`, 1), "task-before.sddl": before} {
		if err = os.WriteFile(filepath.Join(report, name), []byte(value), 0600); err != nil {
			return report, err
		}
	}
	statePath := autostart.StatePath(root)
	if data, e := os.ReadFile(statePath); e == nil {
		if err = os.WriteFile(filepath.Join(report, "autostart-before.json"), data, 0600); err != nil {
			return report, err
		}
	} else if !os.IsNotExist(e) {
		return report, e
	}
	if message("将修复当前用户的旧 YoooClaw 登录任务并恢复连接。\n\n只处理该任务，必要时请求一次管理员授权；不修改任务文件夹权限，不删除数据、不更换密钥。\n会在 CLI 安装目录保留一个原生隐藏启动器。\n\n完成前请等待约 1–2 分钟。是否继续？", 0x24) != 6 {
		return report, fmt.Errorf("用户取消，未修改任务")
	}
	self, err := os.Executable()
	if err != nil {
		return report, err
	}
	hostPath := filepath.Join(bin, "yoooclaw-repair-host.exe")
	data, err := os.ReadFile(self)
	if err != nil {
		return report, err
	}
	if old, e := os.ReadFile(hostPath); e == nil && !bytes.Equal(old, data) {
		if err = os.WriteFile(filepath.Join(report, "repair-host-before.exe"), old, 0600); err != nil {
			return report, err
		}
	} else if e != nil && !os.IsNotExist(e) {
		return report, e
	}
	if err = fsutil.WriteAtomic(hostPath, data, 0755); err != nil {
		return report, err
	}
	newXML := taskrepair.XML(sid, root, hostPath)
	register := func() error {
		return wintask.Call(s.Folder(), "RegisterTask", "yoooclaw-daemon", newXML, 4|16, sid, nil, 3, nil)
	}
	err = register()
	if err != nil {
		l.Printf("Normal registration failed: %s", diagnostics.SafeError(err))
		// Never elevate for arbitrary errors or tool/organization policy blocks.
		if !taskrepair.AccessDenied(err) {
			return report, fmt.Errorf("任务更新失败（未尝试提权）：%w", err)
		}
		if err = elevate(self, sid); err != nil {
			return report, err
		}
		l.Print("User approved task-only DACL repair; folder permissions unchanged")
		if err = s.Refresh(); err != nil {
			return report, err
		}
		if after, e := s.SD(); e == nil {
			_ = os.WriteFile(filepath.Join(report, "task-after.sddl"), []byte(after), 0600)
		}
		if err = register(); err != nil {
			return report, fmt.Errorf("授权后仍无法更新任务，可能还有目录权限或策略限制；未扩大放权：%w", err)
		}
	}
	if err = s.Refresh(); err != nil {
		return report, err
	}
	actual, err := s.XML()
	if err != nil {
		return report, err
	}
	if err = taskrepair.Validate(actual, sid, profile, wintask.ResolveSID); err != nil {
		return report, err
	}
	if !strings.Contains(actual, hostPath) {
		return report, fmt.Errorf("任务动作未更新到新启动器")
	}
	if err = fsutil.WriteJSON(statePath, autostart.State{Version: 1, Desired: "enabled", Manager: "task-scheduler", Unit: taskrepair.TaskPath, Executable: exe}, 0600); err != nil {
		return report, err
	}
	if err = wintask.Call(s.Task(), "Run", nil); err != nil {
		return report, fmt.Errorf("任务已更新，但启动失败：%w", err)
	}
	l.Print("Task updated and Run accepted; not yet claiming connected")
	client := daemon.NewClient(p).WithTimeout(3 * time.Second)
	check := func() (int, error) {
		running, e := s.Running()
		if e != nil {
			return 0, e
		}
		enabled, e := s.Enabled()
		if e != nil {
			return 0, e
		}
		state := daemon.State(p)
		if !running || !enabled || !state.Running || state.Lock == nil {
			return 0, fmt.Errorf("任务或 daemon 尚未运行")
		}
		code, body, e := client.Request("GET", "/daemon/status", nil)
		if e != nil {
			return 0, e
		}
		m, _ := body.(map[string]any)
		relay, _ := m["relay"].(map[string]any)
		if code != 200 || m["ingressMode"] != "standalone" || relay["mode"] != "relay" || relay["connected"] != true {
			return 0, fmt.Errorf("daemon 尚未确认 standalone / Relay connected")
		}
		return state.Lock.PID, nil
	}
	deadline := time.Now().Add(60 * time.Second)
	var pid int
	for {
		pid, err = check()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return report, fmt.Errorf("任务已修复，但连接未通过验收：%w", err)
		}
		time.Sleep(2 * time.Second)
	}
	l.Printf("First verification: PID=%d task running + enabled, Relay connected", pid)
	time.Sleep(60 * time.Second)
	second, e := check()
	if e != nil {
		return report, e
	}
	if second != pid {
		return report, fmt.Errorf("观察期内 daemon PID 发生变化，需要进一步检查")
	}
	l.Printf("SUCCESS: same PID=%d after 60s. Logoff/logon not tested. Old task XML and ACL backed up; historical ACL origin not proven.", pid)
	return report, nil
}

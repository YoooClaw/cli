// Package paths 解析 ~/.yoooclaw/ 目录布局。
//
//	~/.yoooclaw/
//	  credentials.json            account 级共享凭据（跨 profile 且与插件共享）
//	  active-profile              当前 active profile 名（文本）
//	  profiles/<profile>/
//	    config.json
//	    credentials.json          instance 级密文
//	    daemon.lock
//	    logs/daemon.log           daemon 日志 + 轮转文件（logs/daemon.log.YYYY-MM-DD）
//	    notifications/ recordings/ images/ light-rules/ state/
package paths

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultProfile 是缺省 profile 名。
const DefaultProfile = "default"

// DefaultRoot 返回**不读环境变量**的缺省根数据目录（~/.yoooclaw）。
// 供 library（yclib）等需要显式、可预测根目录的调用方使用，避免隐式 env 决策。
func DefaultRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".yoooclaw")
}

// RootDir 返回 CLI 根数据目录。可用 YOOOCLAW_HOME 覆盖（测试 / 多实例隔离）。
// 注意：本函数会读环境变量，仅供 CLI 外壳使用；library 路径请用 DefaultRoot / ForRoot。
func RootDir() string {
	if h := strings.TrimSpace(os.Getenv("YOOOCLAW_HOME")); h != "" {
		return h
	}
	return DefaultRoot()
}

// SharedCredentialsPath 返回 account 级共享凭据文件（顶层，跨 profile + 插件共享）。
func SharedCredentialsPath() string {
	return filepath.Join(RootDir(), "credentials.json")
}

// ActiveProfilePath 返回记录当前 active profile 的文本文件路径。
func ActiveProfilePath() string {
	return filepath.Join(RootDir(), "active-profile")
}

// ProfileDir 返回某个 profile 的目录。
func ProfileDir(profile string) string {
	return filepath.Join(RootDir(), "profiles", profile)
}

// ProfilesRoot 返回 profiles/ 根目录。
func ProfilesRoot() string {
	return filepath.Join(RootDir(), "profiles")
}

// Paths 是某个 profile 下的全部关键路径。
type Paths struct {
	Profile       string
	Dir           string
	Config        string
	Credentials   string
	DaemonLock    string
	Logs          string
	DaemonLog     string
	Notifications string
	Recordings    string
	Images        string
	WebPages      string
	LightRules    string
	State         string
}

// For 解析某个 profile 下的全部关键路径（根目录走 RootDir，含 env 覆盖）。
func For(profile string) Paths {
	return ForRoot(RootDir(), profile)
}

// ForRoot 在**显式根目录**下解析某个 profile 的全部关键路径，不读任何环境变量。
// 供 library（yclib）注入 Config.RootDir 时使用，保证路径可预测、无隐式 env 决策。
func ForRoot(root, profile string) Paths {
	dir := filepath.Join(root, "profiles", profile)
	return Paths{
		Profile:       profile,
		Dir:           dir,
		Config:        filepath.Join(dir, "config.json"),
		Credentials:   filepath.Join(dir, "credentials.json"),
		DaemonLock:    filepath.Join(dir, "daemon.lock"),
		Logs:          filepath.Join(dir, "logs"),
		DaemonLog:     filepath.Join(dir, "logs", "daemon.log"),
		Notifications: filepath.Join(dir, "notifications"),
		Recordings:    filepath.Join(dir, "recordings"),
		Images:        filepath.Join(dir, "images"),
		WebPages:      filepath.Join(dir, "web-pages"),
		LightRules:    filepath.Join(dir, "light-rules"),
		State:         filepath.Join(dir, "state"),
	}
}

// MigrateLogs 把旧布局里直接放在 profile 根目录的 daemon.log（含轮转文件
// daemon.log.YYYY-MM-DD）一次性挪进 logs/ 子目录。已是新布局时为 no-op。
func (p Paths) MigrateLogs() {
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return
	}
	var moved []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "daemon.log" || strings.HasPrefix(name, "daemon.log.") {
			moved = append(moved, name)
		}
	}
	if len(moved) == 0 {
		return
	}
	if err := os.MkdirAll(p.Logs, 0o700); err != nil {
		return
	}
	for _, name := range moved {
		_ = os.Rename(filepath.Join(p.Dir, name), filepath.Join(p.Logs, name))
	}
}

// ListProfileNames 列出已存在的 profile 名（profiles/ 下的目录），按名排序。
func ListProfileNames() []string {
	root := ProfilesRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// ReadActiveProfile 读取有效的 active profile。文件不存在、内容为空，或目标
// profile 目录已被手工删除时返回 ""，让调用方安全回退到 default。
//
// 只校验目录，不要求 config.json 已初始化：profile create 的向导可能正在创建目录，
// profile use 也允许先切换到尚未完成初始化的 profile。
func ReadActiveProfile() string {
	raw, err := os.ReadFile(ActiveProfilePath())
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(raw))
	if name == "" {
		return ""
	}
	info, err := os.Stat(ProfileDir(name))
	if err != nil || !info.IsDir() {
		return ""
	}
	return name
}

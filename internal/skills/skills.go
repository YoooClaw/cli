// Package skills 列出并安装随二进制内嵌的 SKILL.md 到 agent 可发现目录
// （来源为二进制内嵌 FS，而非同级 skills/ 目录）。
package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	assets "github.com/YoooClaw/cli"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

const embedRoot = "skills"

// Bundled 是一个内嵌 Skill 的元数据。
type Bundled struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

var fmBlockRE = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---`)

func pickMeta(block, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.+)$`)
	if m := re.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func parseSkillMeta(md string) (name, description string) {
	block := md
	if m := fmBlockRE.FindStringSubmatch(md); m != nil {
		block = m[1]
	}
	return pickMeta(block, "name"), pickMeta(block, "description")
}

// List 列出所有内嵌 Skill（含 frontmatter 元数据），按名排序。
func List() ([]Bundled, error) {
	entries, err := fs.ReadDir(assets.SkillsFS, embedRoot)
	if err != nil {
		return nil, errs.New(errs.CodeNotFound, "找不到内嵌的 skills 资源")
	}
	var out []Bundled
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mdPath := embedRoot + "/" + e.Name() + "/SKILL.md"
		raw, err := fs.ReadFile(assets.SkillsFS, mdPath)
		if err != nil {
			continue
		}
		name, desc := parseSkillMeta(string(raw))
		title := name
		if title == "" {
			title = e.Name()
		}
		out = append(out, Bundled{Name: e.Name(), Title: title, Description: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// TargetCandidate 是一个可安装目标 agent。
type TargetCandidate struct {
	Agent          string `json:"agent"`
	Label          string `json:"label"`
	HomeDir        string `json:"homeDir"`
	Target         string `json:"target"`
	Detected       bool   `json:"detected"`
	Reason         string `json:"reason"`
	InstallCommand string `json:"installCommand"`
}

func claudeHome() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

func codexHome() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".codex")
}

type adapter struct {
	id, label, envHint string
	home               func() string
}

var adapters = []adapter{
	{id: "claude", label: "Claude Code", home: claudeHome},
	{id: "codex", label: "Codex", envHint: "CODEX_HOME", home: codexHome},
}

func adapterByID(id string) *adapter {
	for i := range adapters {
		if adapters[i].id == id {
			return &adapters[i]
		}
	}
	return nil
}

// Targets 列出 agent 候选目标及自动探测结果。
func Targets() []TargetCandidate {
	out := make([]TargetCandidate, 0, len(adapters))
	for _, a := range adapters {
		home := a.home()
		target := filepath.Join(home, "skills")
		hasHome := fsutil.Exists(home)
		hasTarget := fsutil.Exists(target)
		envVal := ""
		if a.envHint != "" {
			envVal = strings.TrimSpace(os.Getenv(a.envHint))
		}
		reason := "未发现配置目录；可显式传 --agent " + a.id
		switch {
		case hasTarget:
			reason = "skills 目录已存在"
		case hasHome:
			reason = "agent 配置目录已存在"
		case envVal != "" && a.envHint != "":
			reason = a.envHint + " 已设置"
		case a.envHint != "":
			reason = "未发现配置目录；可设置 " + a.envHint + " 或显式传 --agent " + a.id
		}
		out = append(out, TargetCandidate{
			Agent: a.id, Label: a.label, HomeDir: home, Target: target,
			Detected: hasHome || hasTarget || envVal != "", Reason: reason,
			InstallCommand: "yoooclaw skills install --agent " + a.id,
		})
	}
	return out
}

// Selection 是解析后的安装目标。
type Selection struct {
	Agent      string
	AgentLabel string
	Target     string
	Source     string
	Detected   []TargetCandidate
}

// ResolveTarget 解析安装目标（对齐 TS resolveTargetSelection）。
func ResolveTarget(agentRaw, targetRaw string) (Selection, error) {
	agent := strings.TrimSpace(agentRaw)
	if agent == "" {
		agent = "auto"
	}
	valid := map[string]bool{"auto": true, "custom": true, "claude": true, "codex": true}
	if !valid[agent] {
		return Selection{}, errs.New(errs.CodeInvalidArgument, "不支持的 agent："+agent).
			WithHint("可选值：auto | custom | claude | codex")
	}
	explicit := strings.TrimSpace(targetRaw)
	if explicit != "" {
		if agent == "auto" || agent == "custom" {
			return Selection{Agent: "custom", AgentLabel: "Custom", Target: explicit, Source: "explicit-target"}, nil
		}
		a := adapterByID(agent)
		return Selection{Agent: agent, AgentLabel: a.label, Target: explicit, Source: "explicit-target"}, nil
	}
	if agent == "custom" {
		return Selection{}, errs.New(errs.CodeInvalidArgument, "`--agent custom` 需要同时传 `--target <dir>`").
			WithHint("例如：yoooclaw skills install --agent custom --target ~/.config/agent/skills")
	}
	if agent != "auto" {
		a := adapterByID(agent)
		return Selection{Agent: agent, AgentLabel: a.label, Target: filepath.Join(a.home(), "skills"), Source: "agent-default"}, nil
	}
	candidates := Targets()
	var detected []TargetCandidate
	for _, c := range candidates {
		if c.Detected {
			detected = append(detected, c)
		}
	}
	if len(detected) == 1 {
		only := detected[0]
		return Selection{Agent: only.Agent, AgentLabel: only.Label, Target: only.Target, Source: "auto-detected", Detected: detected}, nil
	}
	if len(detected) == 0 {
		return Selection{}, errs.New(errs.CodeNotFound, "未检测到可安装 Skill 的 Agent",
			map[string]any{"hint": "请显式指定 `--agent claude` / `--agent codex`，或使用 `--target <dir>`"})
	}
	return Selection{}, errs.New(errs.CodeConfirmationRequired, "检测到多个 Agent，无法自动判断 Skill 应安装到哪里",
		map[string]any{"hint": "请显式指定 `--agent claude` / `--agent codex`，或使用 `--target <dir>`"})
}

// InstallResult 是单个 Skill 的安装结果。
type InstallResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // installed | skipped
	Reason string `json:"reason,omitempty"`
	Dest   string `json:"dest"`
}

// Install 把内嵌 Skill 复制到 target；force 时覆盖已存在目录。
//
// 与 TS 版不同：内嵌资源无法软链回源，故一律复制；升级 CLI 后需重跑本命令刷新。
func Install(target string, force bool) ([]InstallResult, []string, error) {
	skills, err := List()
	if err != nil {
		return nil, nil, err
	}
	if err := fsutil.EnsureDir(target, fsutil.DirMode); err != nil {
		return nil, nil, err
	}
	var results []InstallResult
	var installed []string
	for _, s := range skills {
		dest := filepath.Join(target, s.Name)
		if fsutil.Exists(dest) {
			if !force {
				results = append(results, InstallResult{Name: s.Name, Status: "skipped", Reason: "目标已存在，加 --force 覆盖", Dest: dest})
				continue
			}
			if err := os.RemoveAll(dest); err != nil {
				return nil, nil, err
			}
		}
		if err := extractSkill(s.Name, dest); err != nil {
			return nil, nil, err
		}
		results = append(results, InstallResult{Name: s.Name, Status: "installed", Dest: dest})
		installed = append(installed, s.Name)
	}
	return results, installed, nil
}

// extractSkill 把内嵌的 skills/<name>/** 写到 dest。
func extractSkill(name, dest string) error {
	srcRoot := embedRoot + "/" + name
	return fs.WalkDir(assets.SkillsFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, srcRoot)
		rel = strings.TrimPrefix(rel, "/")
		outPath := dest
		if rel != "" {
			outPath = filepath.Join(dest, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return fsutil.EnsureDir(outPath, fsutil.DirMode)
		}
		data, err := fs.ReadFile(assets.SkillsFS, p)
		if err != nil {
			return err
		}
		if err := fsutil.EnsureDir(filepath.Dir(outPath), fsutil.DirMode); err != nil {
			return err
		}
		return os.WriteFile(outPath, data, fsutil.ConfigFileMode)
	})
}

package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/skills"
	"github.com/spf13/cobra"
)

func newSkillsCmd() *cobra.Command {
	c := &cobra.Command{Use: "skills", Short: "Agent 技能管理：把随包发布的 SKILL.md 装到 agent 可发现目录 🟢"}

	listCmd := &cobra.Command{Use: "list", Short: "列出随 CLI 发布的内置 Skill 及其触发说明", Args: cobra.NoArgs, RunE: run(skillsList)}
	targetsCmd := &cobra.Command{Use: "targets", Short: "列出支持的 Agent skills 目录与自动探测结果", Args: cobra.NoArgs, RunE: run(skillsTargets)}
	installCmd := &cobra.Command{Use: "install", Short: "把内置 Skill 安装到 agent skills 目录（默认自动探测）", Args: cobra.NoArgs, RunE: run(skillsInstall)}
	installCmd.Flags().String("agent", "auto", "安装目标 agent：auto|claude|codex|custom")
	installCmd.Flags().String("target", "", "安装目标目录；传入后优先于自动探测")
	installCmd.Flags().Bool("copy", false, "复制（内嵌资源恒为复制，本 flag 仅兼容）")
	installCmd.Flags().Bool("force", false, "目标已存在同名 Skill 时覆盖")

	c.AddCommand(listCmd, targetsCmd, installCmd)
	return c
}

func skillsList(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	list, err := skills.List()
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(list))
	for _, s := range list {
		items = append(items, map[string]any{"name": s.Name, "title": s.Title, "description": s.Description})
	}
	return map[string]any{
		"ok": true, "count": len(list), "skills": items,
		"hint": "用 `yoooclaw skills targets` 查看可安装目标，再用 `yoooclaw skills install --agent <agent>` 安装",
	}, nil
}

func skillsTargets(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	targets := skills.Targets()
	items := make([]any, 0, len(targets))
	for _, t := range targets {
		items = append(items, map[string]any{
			"agent": t.Agent, "label": t.Label, "homeDir": t.HomeDir, "target": t.Target,
			"detected": t.Detected, "reason": t.Reason, "installCommand": t.InstallCommand,
		})
	}
	return map[string]any{
		"ok": true, "targets": items,
		"hint": "裸 `yoooclaw skills install` 只会在检测到唯一 Agent 时自动安装；否则请显式传 `--agent` 或 `--target`",
	}, nil
}

func skillsInstall(_ *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	sel, err := skills.ResolveTarget(flagStr(cmd, "agent"), flagStr(cmd, "target"))
	if err != nil {
		return nil, err
	}
	results, installed, err := skills.Install(sel.Target, flagBool(cmd, "force"))
	if err != nil {
		return nil, err
	}
	resultsAny := make([]any, 0, len(results))
	skipped := make([]any, 0)
	for _, r := range results {
		m := map[string]any{"name": r.Name, "status": r.Status, "dest": r.Dest}
		if r.Reason != "" {
			m["reason"] = r.Reason
		}
		resultsAny = append(resultsAny, m)
		if r.Status == "skipped" {
			skipped = append(skipped, m)
		}
	}
	hint := "没有新安装的 Skill（已存在则加 --force 覆盖）"
	if len(installed) > 0 {
		hint = "重启 agent 会话后即可被发现；升级 CLI 后请重跑本命令刷新"
	}
	return map[string]any{
		"ok": true, "agent": sel.Agent, "agentLabel": sel.AgentLabel, "target": sel.Target,
		"targetSource": sel.Source, "mode": "copy", "installed": installed,
		"skipped": skipped, "results": resultsAny, "hint": hint,
	}, nil
}

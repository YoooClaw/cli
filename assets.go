// Package assets 把随包发布的静态资源（SKILL.md 技能）通过 go:embed 编译进二进制。
//
// Go 重写后 CLI 是单一二进制，没有 TS 版那种 <pkg>/skills/ 同级目录可供解析与软链；
// 因此把 skills/ 整个嵌入二进制，由 `skills install` 解包（复制）到 agent 技能目录。
package assets

import "embed"

// SkillsFS 内嵌 skills/ 目录（含各 yoooclaw-*/SKILL.md）。
//
//go:embed all:skills
var SkillsFS embed.FS

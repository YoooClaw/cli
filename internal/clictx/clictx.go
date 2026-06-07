// Package clictx 把全局 flags 物化成贯穿命令的执行上下文。
package clictx

import (
	"os"
	"strings"

	"github.com/YoooClaw/cli/internal/output"
	"github.com/YoooClaw/cli/internal/paths"
)

// Context 是贯穿所有命令的执行上下文。
type Context struct {
	Profile string
	Paths   paths.Paths
	Format  output.Format
	Quiet   bool
	Color   bool
}

// ResolveActiveProfile 解析当前 active profile：
// --profile > $YOOOCLAW_PROFILE > active-profile 文件 > default。
func ResolveActiveProfile(flagProfile string) string {
	if s := strings.TrimSpace(flagProfile); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("YOOOCLAW_PROFILE")); s != "" {
		return s
	}
	if s := paths.ReadActiveProfile(); s != "" {
		return s
	}
	return paths.DefaultProfile
}

// Build 构造 CliContext。
func Build(flagProfile, format string, quiet, color bool) (*Context, error) {
	profile := ResolveActiveProfile(flagProfile)
	fmtResolved, err := output.ResolveFormat(format, output.StdoutIsTTY())
	if err != nil {
		return nil, err
	}
	return &Context{
		Profile: profile,
		Paths:   paths.For(profile),
		Format:  fmtResolved,
		Quiet:   quiet,
		Color:   color,
	}, nil
}

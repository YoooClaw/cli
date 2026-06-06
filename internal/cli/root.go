// Package cli 装配 yoooclaw CLI 的命令树（cobra），对齐 TS 版 command-tree + program.ts。
package cli

import (
	"fmt"
	"os"

	"github.com/YoooClaw/cli/internal/version"
	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "yoooclaw",
		Short:         "yoooclaw CLI",
		Long:          "yoooclaw —— 独立守护进程 + CLI：接收手机通知、Relay 隧道、灯效规则评估（Agent-Native）。",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")

	// 全局 flags（对所有命令生效）。
	root.PersistentFlags().String("profile", "", "切换 profile（默认 default）")
	root.PersistentFlags().String("format", "", "输出格式：json|pretty|table|ndjson（TTY 默认 pretty，管道默认 json）")
	root.PersistentFlags().Bool("quiet", false, "抑制进度日志，只输出最终结果")
	root.PersistentFlags().Bool("no-color", false, "关闭终端颜色")

	root.AddCommand(
		newConfigCmd(),
		newProfileCmd(),
		newAuthCmd(),
		newDaemonCmd(),
		newNotificationCmd(),
		newRecordingCmd(),
		newImageCmd(),
		newMonitorCmd(),
		newTunnelCmd(),
		newGatewayCmd(),
		newAPICmd(),
		newLogCmd(),
		newMigrateCmd(),
		newUpdateCmd(),
		newSkillsCmd(),
		newDoctorCmd(),
	)
	return root
}

// Execute 构建 root 命令并运行。
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	c := &cobra.Command{Use: "update", Short: "版本检查 🟢"}
	self := &cobra.Command{Use: "self", Short: "检查灰度平台允许的版本（不自动更新）", Args: cobra.NoArgs, RunE: run(updateSelf)}
	self.Flags().Bool("beta", false, "兼容旧参数；版本资格统一由灰度平台决定")
	self.Flags().Bool("json", false, "只输出版本信息 JSON")
	c.AddCommand(self)
	addRolloutCommands(c)
	return c
}

func updateSelf(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	return updateCheck(ctx, cmd, args)
}

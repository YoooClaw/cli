package cli

import "github.com/spf13/cobra"

func newInstallCmd() *cobra.Command {
	c := &cobra.Command{Use: "install", Short: "原生安装管理"}
	native := &cobra.Command{Use: "native", Short: "将本程序原生安装到当前 Windows 用户目录（无需脚本）", Args: cobra.NoArgs, RunE: run(installNative)}
	native.Flags().Bool("yes", false, "确认安装")
	native.Flags().Bool("force", false, "覆盖已有 CLI，保留配置与数据")
	native.Flags().String("install-dir", "", "安装目录（默认 LOCALAPPDATA\\YoooClaw\\bin）")
	native.Flags().Bool("no-modify-path", false, "不修改当前用户 PATH")
	native.Flags().Bool("activate", false, "安装完成后释放其他 owner 并激活独立 CLI")
	native.Flags().Bool("no-start", false, "不恢复运行，只注册用户登录自启")
	native.Flags().String("hermes-profile", "", "显式选择 Hermes profile")
	c.AddCommand(native)
	return c
}

func setupArgs(args []string) []string {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		return args
	}
	return append([]string{"install", "native"}, args...)
}

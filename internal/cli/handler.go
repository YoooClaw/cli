package cli

import (
	"os"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/output"
	"github.com/spf13/cobra"
)

// exitCode 记录命令执行后的进程退出码（错误时由 wrapper 设置），由 Execute 读取。
var exitCode int

// handler 是命令实现签名：拿到上下文 + cobra 命令/位置参数，返回可序列化结果或错误。
type handler func(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error)

// flagStr 读取（含继承的全局）字符串 flag。
func flagStr(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// flagBool 读取（含继承的全局）布尔 flag；flag 不存在返回 false。
func flagBool(cmd *cobra.Command, name string) bool {
	if cmd.Flags().Lookup(name) == nil {
		return false
	}
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// buildContext 从命令的全局 flags 物化 CliContext。
func buildContext(cmd *cobra.Command) (*clictx.Context, error) {
	color := true
	if cmd.Flags().Lookup("no-color") != nil {
		noColor, _ := cmd.Flags().GetBool("no-color")
		color = !noColor
	}
	return clictx.Build(flagStr(cmd, "profile"), flagStr(cmd, "format"), flagBool(cmd, "quiet"), color)
}

// run 把 handler 包成 cobra RunE：构建 ctx → 执行 → 统一渲染结果/错误并设置退出码。
// 本地 --json flag 作为全局 --format json 的兼容别名（doctor / update self 用）。
func run(h handler) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, err := buildContext(cmd)
		format := output.JSON
		if ctx != nil {
			format = ctx.Format
		}
		if flagBool(cmd, "json") {
			format = output.JSON
		}
		if err != nil {
			output.RenderError(os.Stdout, err, format)
			exitCode = 1
			return nil
		}
		result, herr := h(ctx, cmd, args)
		if herr != nil {
			output.RenderError(os.Stdout, herr, format)
			exitCode = exitCodeOf(herr)
			return nil
		}
		output.RenderResult(os.Stdout, result, format)
		return nil
	}
}

func exitCodeOf(err error) int {
	if e, ok := err.(*errs.Error); ok && e.ExitCode != 0 {
		return e.ExitCode
	}
	return 1
}

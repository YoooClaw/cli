package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/nanoshell"
	"github.com/spf13/cobra"
)

func newNanoshellCmd() *cobra.Command {
	c := &cobra.Command{Use: "nanoshell", Short: "硬件屏幕程序发布与查询"}
	publish := &cobra.Command{Use: "publish", Short: "将已确认的 ZIP 发布给指定客户端", Args: cobra.NoArgs, RunE: run(nanoshellPublish)}
	publish.Flags().String("package", "", "已测试并经用户确认的安装 ZIP")
	publish.Flags().String("client", "", "目标 API-key 客户端 label（必填）")
	_ = publish.MarkFlagRequired("package")
	_ = publish.MarkFlagRequired("client")
	list := &cobra.Command{Use: "list", Short: "列出已发布程序", Args: cobra.NoArgs, RunE: run(func(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
		limit, _ := cmd.Flags().GetInt("limit")
		return (nanoshell.Store{Root: ctx.Paths.Nanoshell}).List(flagStr(cmd, "client"), limit, flagStr(cmd, "cursor"))
	})}
	list.Flags().String("client", "", "仅查询指定客户端；省略则查看本机全部")
	list.Flags().Int("limit", 20, "每页 1～100 条")
	list.Flags().String("cursor", "", "下一页游标")
	location := &cobra.Command{Use: "storage-path", Short: "查看程序存储目录", Args: cobra.NoArgs, RunE: run(func(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
		return map[string]any{"path": ctx.Paths.Nanoshell}, nil
	})}
	c.AddCommand(publish, list, location)
	return c
}
func nanoshellPublish(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	label := flagStr(cmd, "client")
	known := false
	for _, entry := range creds.ResolveAPIKeyEntries().Entries {
		if entry.Label == label {
			known = true
			break
		}
	}
	if !known {
		return nil, errs.New(errs.CodeInvalidArgument, "--client 必须是 auth 中已配置的客户端 label")
	}
	result, err := (nanoshell.Store{Root: ctx.Paths.Nanoshell}).Publish(flagStr(cmd, "package"), label)
	if err != nil {
		return nil, errs.New(nanoshell.Code(err), err.Error())
	}
	return result, nil
}

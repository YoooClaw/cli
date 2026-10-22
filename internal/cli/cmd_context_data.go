package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/contextdata"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/spf13/cobra"
	"time"
)

var contextQueryNow = time.Now

func newContextDataCmd(name, source string) *cobra.Command {
	root := &cobra.Command{Use: name, Short: "查询本地已同步数据（只读）"}
	modes := []string{"list", "get"}
	if source == "health-data" {
		modes = append(modes, "series")
	}
	for _, mode := range modes {
		use := mode
		args := cobra.NoArgs
		if mode == "get" && source != "health-data" {
			use += " <id>"
			args = cobra.ExactArgs(1)
		}
		c := &cobra.Command{Use: use, Args: args, Short: "读取已同步数据及覆盖范围", RunE: run(func(ctx *clictx.Context, c *cobra.Command, args []string) (any, error) {
			limit, e := contextdata.ParseLimit(flagStr(c, "limit"))
			if e != nil {
				return nil, errs.New(errs.CodeInvalidArgument, e.Error())
			}
			o := contextdata.Options{All: flagBool(c, "all"), Date: flagStr(c, "date"), From: flagStr(c, "from-date"), To: flagStr(c, "to-date"), Limit: limit, Cursor: flagStr(c, "cursor"), Metric: flagStr(c, "metric")}
			if mode == "get" && source == "health-data" && o.Date == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "--date is required")
			}
			if mode == "series" && o.Metric == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "--metric is required")
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return contextdata.Query(ctx.Paths, source, mode, id, o, contextQueryNow())
		})}
		if name == "calendar" && mode == "list" {
			c.Short = "默认查询尚未开始的日程；--all 查询全部已同步日程"
		}
		c.Flags().Bool("all", false, "全部已同步数据，含过去、进行中和未来；与日期范围互斥")
		c.Flags().String("date", "", "来源日期 YYYY-MM-DD（位置按接收日）")
		c.Flags().String("from-date", "", "起始日期，包含")
		c.Flags().String("to-date", "", "结束日期，包含")
		c.Flags().String("limit", "100", "每页条数 1..500")
		c.Flags().String("cursor", "", "上一页 nextCursor；保持查询条件一致")
		if mode == "series" {
			c.Flags().String("metric", "", "稳定指标名，例如 steps")
		}
		root.AddCommand(c)
	}
	return root
}

package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	c := &cobra.Command{Use: "sync", Short: "通知同步给记忆系统 🟢"}

	scan := &cobra.Command{
		Use: "scan", Short: "扫描未处理通知，默认只返回本地当天待同步摘要",
		Args: cobra.NoArgs, RunE: run(syncScan),
	}
	addScopeFlags(scan)

	next := &cobra.Command{
		Use: "next", Short: "通用批次迭代器：返回范围内下一批未处理通知（≤100 条）及 commitCommand；处理完返回 done=true",
		Args: cobra.NoArgs, RunE: run(syncNext),
	}
	addScopeFlags(next)

	fetch := &cobra.Command{
		Use: "fetch", Short: "获取指定日期未处理通知详情",
		Args: cobra.NoArgs, RunE: run(syncFetch),
	}
	fetch.Flags().String("date", "", "目标日期（必填，YYYY-MM-DD）")
	fetch.Flags().String("max-end-index", "", "本次快照允许读取的最大 endIndex")

	commit := &cobra.Command{
		Use: "commit", Short: "标记指定日期当前批次处理完成",
		Args: cobra.NoArgs, RunE: run(syncCommit),
	}
	commit.Flags().String("date", "", "目标日期（必填，YYYY-MM-DD）")
	commit.Flags().String("end-index", "", "本批次 fetch 返回的 endIndex")

	c.AddCommand(scan, next, fetch, commit)
	return c
}

// addScopeFlags 给 scan / next 挂上统一的日期范围筛选 flags。
func addScopeFlags(c *cobra.Command) {
	c.Flags().Bool("all", false, "处理 checkpoint 之后所有日期的未处理通知")
	c.Flags().String("date", "", "只处理指定日期 YYYY-MM-DD")
	c.Flags().String("from-date", "", "只处理该日期及之后的数据 YYYY-MM-DD")
	c.Flags().String("to-date", "", "只处理该日期及之前的数据 YYYY-MM-DD")
}

// resolveScope 从命令 flags 解析同步范围（缺省本地当天）。
func resolveScope(cmd *cobra.Command) (notif.SyncScope, error) {
	return notif.ResolveScanScope(notif.SyncScopeOptions{
		All:      flagBool(cmd, "all"),
		Date:     flagStr(cmd, "date"),
		FromDate: flagStr(cmd, "from-date"),
		ToDate:   flagStr(cmd, "to-date"),
	})
}

func syncScan(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	scope, err := resolveScope(cmd)
	if err != nil {
		return nil, err
	}
	return notif.ScanSync(ctx.Paths.Notifications, scope), nil
}

func syncNext(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	scope, err := resolveScope(cmd)
	if err != nil {
		return nil, err
	}
	return notif.NextSync(ctx.Paths.Notifications, scope), nil
}

func syncFetch(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	date := flagStr(cmd, "date")
	if date == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--date 必填（YYYY-MM-DD）")
	}
	return notif.FetchSync(ctx.Paths.Notifications, date, flagStr(cmd, "max-end-index"))
}

func syncCommit(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	date := flagStr(cmd, "date")
	if date == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--date 必填（YYYY-MM-DD）")
	}
	return notif.CommitSync(ctx.Paths.Notifications, date, flagStr(cmd, "end-index"))
}

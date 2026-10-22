package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/contextdata"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	c := &cobra.Command{Use: "sync", Short: "将同步数据分批交给记忆系统；默认来源为通知 🟢"}

	scan := &cobra.Command{
		Use: "scan", Short: "扫描所选来源的待处理数据，默认本地当天",
		Args: cobra.NoArgs, RunE: run(syncScan),
	}
	addScopeFlags(scan)

	next := &cobra.Command{
		Use: "next", Short: "返回所选来源的下一批数据与确认参数；通知用下标，其他来源用 token",
		Args: cobra.NoArgs, RunE: run(syncNext),
	}
	addScopeFlags(next)

	fetch := &cobra.Command{
		Use: "fetch", Short: "获取指定日期未处理通知详情",
		Args: cobra.NoArgs, RunE: run(syncFetch),
	}
	fetch.Flags().String("date", "", "通知日期（YYYY-MM-DD）")
	fetch.Flags().String("max-end-index", "", "本次快照允许读取的最大 endIndex")

	commit := &cobra.Command{
		Use: "commit", Short: "标记指定日期当前批次处理完成",
		Args: cobra.NoArgs, RunE: run(syncCommit),
	}
	commit.Flags().String("date", "", "通知日期（YYYY-MM-DD）")
	commit.Flags().String("end-index", "", "本批次 fetch 返回的 endIndex")

	c.PersistentFlags().String("source", "notifications", "notifications|calendars|locations|health-data|web-pages")
	commit.Flags().String("token", "", "next 返回的精确快照批次 token（非通知来源）")
	c.AddCommand(scan, next, fetch, commit)
	return c
}

// addScopeFlags 给 scan / next 挂上统一的日期范围筛选 flags。
func addScopeFlags(c *cobra.Command) {
	c.Flags().Bool("all", false, "处理所有日期的未确认数据与内容修订")
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
	if source := flagStr(cmd, "source"); source != "notifications" {
		scope, err := resolveScope(cmd)
		if err != nil {
			return nil, err
		}
		return contextdata.Next(ctx.Paths, source, contextdata.Options{All: scope.Mode == "all", From: scope.FromDate, To: scope.ToDate}, true)
	}
	scope, err := resolveScope(cmd)
	if err != nil {
		return nil, err
	}
	return notif.ScanSync(ctx.Paths.Notifications, scope), nil
}

func syncNext(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if source := flagStr(cmd, "source"); source != "notifications" {
		scope, err := resolveScope(cmd)
		if err != nil {
			return nil, err
		}
		return contextdata.Next(ctx.Paths, source, contextdata.Options{All: scope.Mode == "all", From: scope.FromDate, To: scope.ToDate}, false)
	}
	scope, err := resolveScope(cmd)
	if err != nil {
		return nil, err
	}
	return notif.NextSync(ctx.Paths.Notifications, scope), nil
}

func syncFetch(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if flagStr(cmd, "source") != "notifications" {
		return nil, errs.New(errs.CodeInvalidArgument, "snapshot sources use sync next")
	}
	date := flagStr(cmd, "date")
	if date == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--date 必填（YYYY-MM-DD）")
	}
	return notif.FetchSync(ctx.Paths.Notifications, date, flagStr(cmd, "max-end-index"))
}

func syncCommit(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if source := flagStr(cmd, "source"); source != "notifications" {
		if flagStr(cmd, "date") != "" || flagStr(cmd, "end-index") != "" {
			return nil, errs.New(errs.CodeInvalidArgument, "snapshot commit requires only --token")
		}
		return contextdata.Commit(ctx.Paths, source, flagStr(cmd, "token"))
	}
	if flagStr(cmd, "token") != "" {
		return nil, errs.New(errs.CodeInvalidArgument, "notification commit requires --date and --end-index")
	}
	date := flagStr(cmd, "date")
	if date == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--date 必填（YYYY-MM-DD）")
	}
	return notif.CommitSync(ctx.Paths.Notifications, date, flagStr(cmd, "end-index"))
}

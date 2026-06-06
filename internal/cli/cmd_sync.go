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
		Use: "scan", Short: "扫描未处理通知，返回各日期待同步摘要",
		Args: cobra.NoArgs, RunE: run(syncScan),
	}

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

	c.AddCommand(scan, fetch, commit)
	return c
}

func syncScan(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return notif.ScanSync(ctx.Paths.Notifications), nil
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

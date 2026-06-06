package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/logread"
	"github.com/spf13/cobra"
)

func newLogCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "log [keyword]",
		Short: "日志检索 🟢",
		Args:  cobra.MaximumNArgs(1),
		RunE:  run(logSearch),
	}
	c.Flags().String("from", "", "YYYY-MM-DD，默认 7 天前")
	c.Flags().String("to", "", "YYYY-MM-DD，默认今天")
	c.Flags().String("limit", "50", "最大返回条数")
	c.Flags().String("level", "", "过滤日志级别")

	errorsCmd := &cobra.Command{Use: "+errors", Short: "昨天起的 error 级日志", Args: cobra.NoArgs, RunE: run(logErrors)}
	c.AddCommand(errorsCmd)
	return c
}

func logSearch(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	keyword := ""
	if len(args) > 0 {
		keyword = args[0]
	}
	from := flagStr(cmd, "from")
	if from == "" {
		from = localDaysAgo(7)
	}
	to := flagStr(cmd, "to")
	if to == "" {
		to = localToday()
	}
	lines := logread.Search(ctx.Paths.DaemonLog, logread.Query{
		Keyword: keyword, Level: flagStr(cmd, "level"), From: from, To: to,
		Limit: atoiDefault(flagStr(cmd, "limit"), 50),
	})
	return map[string]any{"ok": true, "keyword": nilIfEmpty(keyword), "total": len(lines), "lines": lines}, nil
}

func logErrors(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	lines := logread.Search(ctx.Paths.DaemonLog, logread.Query{
		Level: "error", From: localDaysAgo(1), To: localToday(), Limit: 50,
	})
	return map[string]any{"ok": true, "level": "error", "total": len(lines), "lines": lines}, nil
}

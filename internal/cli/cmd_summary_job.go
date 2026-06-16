package cli

import (
	"os"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/summaryjob"
	"github.com/spf13/cobra"
)

func newSummaryJobCmd() *cobra.Command {
	c := &cobra.Command{Use: "summary-job", Short: "分片通知总结任务：大批量通知切片→逐片总结→合并结果 🟢"}

	create := &cobra.Command{Use: "create", Short: "创建分片通知总结任务", Args: cobra.NoArgs, RunE: run(summaryJobCreate)}
	addQueryFlagsWithLimit(create, strconv.Itoa(summaryjob.DefaultJobLimit))
	create.Flags().String("chunk-size", strconv.Itoa(summaryjob.DefaultChunkSize), "每个分片的通知条数")
	create.Flags().String("max-content", strconv.Itoa(summaryjob.DefaultMaxContent), "分片中单条通知标题/正文最大字数")

	status := &cobra.Command{Use: "status <id>", Short: "查看任务状态", Args: cobra.ExactArgs(1), RunE: run(summaryJobStatus)}
	next := &cobra.Command{Use: "next <id>", Short: "领取或重试下一个待总结分片", Args: cobra.ExactArgs(1), RunE: run(summaryJobNext)}

	commit := &cobra.Command{Use: "commit <id>", Short: "提交分片摘要并标记分片完成", Args: cobra.ExactArgs(1), RunE: run(summaryJobCommit)}
	commit.Flags().String("chunk-id", "", "next 返回的分片 id（必填）")
	commit.Flags().String("summary", "", "直接传入分片摘要文本")
	commit.Flags().String("summary-file", "", "读取文件内容作为分片摘要")

	runc := &cobra.Command{Use: "run <id>", Short: "用抽取式摘要自动处理待总结分片（无需模型）", Args: cobra.ExactArgs(1), RunE: run(summaryJobRun)}
	runc.Flags().String("max-chunks", strconv.Itoa(summaryjob.DefaultRunMaxChunks), "本次最多自动处理的分片数")
	runc.Flags().Bool("include-result", false, "完成时在输出中包含 markdown 结果")

	result := &cobra.Command{Use: "result <id>", Short: "合并已提交的分片摘要并返回结果", Args: cobra.ExactArgs(1), RunE: run(summaryJobResult)}
	cancel := &cobra.Command{Use: "cancel <id>", Short: "取消任务（保留已落盘分片与摘要）", Args: cobra.ExactArgs(1), RunE: run(summaryJobCancel)}

	c.AddCommand(create, status, next, commit, runc, result, cancel)
	return c
}

func parsePositiveInt(cmd *cobra.Command, flag string, def int) (int, error) {
	raw := flagStr(cmd, flag)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || strconv.Itoa(n) != raw {
		return 0, errs.Newf(errs.CodeInvalidArgument, "--%s 必须是大于 0 的整数", flag)
	}
	return n, nil
}

func summaryJobCreate(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	raw := rawQueryFromCmd(cmd)
	if raw.Limit == "" {
		raw.Limit = strconv.Itoa(summaryjob.DefaultJobLimit)
	}
	opts, err := notif.BuildQueryOptions(raw, summaryjob.DefaultJobLimit)
	if err != nil {
		return nil, err
	}
	chunkSize, err := parsePositiveInt(cmd, "chunk-size", summaryjob.DefaultChunkSize)
	if err != nil {
		return nil, err
	}
	maxContent, err := parsePositiveInt(cmd, "max-content", summaryjob.DefaultMaxContent)
	if err != nil {
		return nil, err
	}
	return summaryjob.Create(ctx.Paths.Notifications, summaryjob.CreateParams{
		Query: opts,
		Meta: summaryjob.QueryMeta{
			From: raw.From, To: raw.To, App: raw.App, Sender: raw.Sender,
			ConversationType: raw.ConversationType, Keyword: raw.Keyword, Limit: opts.Limit,
		},
		ChunkSize: chunkSize, MaxContent: maxContent,
	})
}

func summaryJobStatus(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return summaryjob.Status(ctx.Paths.Notifications, args[0])
}

func summaryJobNext(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return summaryjob.Next(ctx.Paths.Notifications, args[0])
}

func summaryJobCommit(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	chunkID := flagStr(cmd, "chunk-id")
	if chunkID == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--chunk-id 必填")
	}
	text, err := resolveSummaryText(cmd)
	if err != nil {
		return nil, err
	}
	return summaryjob.Commit(ctx.Paths.Notifications, args[0], chunkID, text)
}

func resolveSummaryText(cmd *cobra.Command) (string, error) {
	summary := flagStr(cmd, "summary")
	summaryFile := flagStr(cmd, "summary-file")
	if summary != "" && summaryFile != "" {
		return "", errs.New(errs.CodeInvalidArgument, "--summary 和 --summary-file 只能二选一")
	}
	if summary == "" && summaryFile == "" {
		return "", errs.New(errs.CodeInvalidArgument, "必须提供 --summary 或 --summary-file")
	}
	if summary != "" {
		return summary, nil
	}
	raw, err := os.ReadFile(summaryFile)
	if err != nil {
		return "", errs.Newf(errs.CodeNotFound, "无法读取 --summary-file: %s", summaryFile)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", errs.New(errs.CodeInvalidArgument, "分片摘要不能为空")
	}
	return string(raw), nil
}

func summaryJobRun(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	maxChunks, err := parsePositiveInt(cmd, "max-chunks", summaryjob.DefaultRunMaxChunks)
	if err != nil {
		return nil, err
	}
	return summaryjob.Run(ctx.Paths.Notifications, args[0], maxChunks, flagBool(cmd, "include-result"))
}

func summaryJobResult(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return summaryjob.Result(ctx.Paths.Notifications, args[0])
}

func summaryJobCancel(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return summaryjob.Cancel(ctx.Paths.Notifications, args[0])
}

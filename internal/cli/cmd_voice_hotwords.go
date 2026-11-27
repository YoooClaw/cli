package cli

import (
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/output"
	"github.com/YoooClaw/cli/internal/voice"
	"github.com/spf13/cobra"
)

const hotwordBatchHelp = `多个词用空格分隔，含空格的词用引号括起。CLI 按输入顺序逐词提交。
遇到词条冲突（409）或校验失败（422）时继续处理后续词。鉴权失败（401/403）、网络或服务异常时停止。
已写入的词会保留。部分失败时退出码非零，结果按词标记 ok（成功）、error（出错）或 not_attempted（未执行）。`

func newVoiceHotwordsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "hotwords",
		Short: "管理当前账号的云端语音识别热词",
		Long:  "查询、添加或删除当前账号的云端语音识别热词。\n\n" + hotwordBatchHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		Example: `  yoooclaw voice hotwords list --format json
  yoooclaw voice hotwords add "星桥" "YoooClaw Capture"
  yoooclaw voice hotwords delete "星桥" "YoooClaw Capture"`,
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "按页查看有效热词",
		Args:  cobra.NoArgs,
		RunE:  run(voiceHotwordList),
		Long:  "每次读取一页有效热词。JSON 输出中，count 是本页实际返回的词数，空页为 0。items 中每个词条的 text 字段是热词原文。\n有下一页时，将 next_cursor 原样传给 --cursor。末页省略 next_cursor。查询词库总数时，累加各页的 count。NDJSON 仅逐行输出当前页的词条。需要翻页或统计数量时，请用 --format json 获取 next_cursor 和 count。",
		Example: `  yoooclaw voice hotwords list --limit 20 --format json
  yoooclaw voice hotwords list --limit 20 --cursor 42 --format json`,
	}
	list.Flags().Int("limit", 50, "每页词数，范围 1 到 100")
	list.Flags().Int64("cursor", 0, "上一页返回的 next_cursor，首次查询可省略")
	add := &cobra.Command{
		Use:     "add <词> [词...]",
		Short:   "添加一个或多个热词",
		Args:    cobra.MinimumNArgs(1),
		RunE:    run(voiceHotwordMutate),
		Long:    "按原文添加热词。重复添加已有词也返回成功。\n\n" + hotwordBatchHelp,
		Example: `  yoooclaw voice hotwords add "星桥" "YoooClaw Capture"`,
	}
	del := &cobra.Command{
		Use:     "delete <词> [词...]",
		Aliases: []string{"remove"},
		Short:   "删除一个或多个热词",
		Args:    cobra.MinimumNArgs(1),
		RunE:    run(voiceHotwordMutate),
		Long:    "按提交的词文本删除当前账号的云端词条。词条已不存在时也返回成功。\n\n" + hotwordBatchHelp,
		Example: `  yoooclaw voice hotwords delete "星桥" "YoooClaw Capture"`,
	}
	group.AddCommand(list, add, del)
	return group
}

func voiceHotwordClient(ctx *clictx.Context) voice.HotwordClient {
	return voice.HotwordClient{Host: cloudHost(ctx), APIKey: creds.ResolveAPIKey().Value}
}

func voiceHotwordList(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	limit, _ := cmd.Flags().GetInt("limit")
	cursor, _ := cmd.Flags().GetInt64("cursor")
	if limit < 1 || limit > 100 || cursor < 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "--limit 须为 1 到 100 的整数，--cursor 须为非负整数")
	}
	page, err := voiceHotwordClient(ctx).List(cmd.Context(), limit, cursor)
	if err != nil {
		return nil, err
	}
	rows := make([]any, 0, len(page.Items))
	for _, word := range page.Items {
		rows = append(rows, map[string]any{"text": word.Text})
	}
	if ctx.Format == output.NDJSON {
		return rows, nil
	}
	result := map[string]any{"ok": true, "count": len(rows), "items": rows}
	if page.NextCursor != nil {
		result["next_cursor"] = *page.NextCursor
	}
	return result, nil
}

func voiceHotwordMutate(ctx *clictx.Context, cmd *cobra.Command, words []string) (any, error) {
	action := cmd.Name()
	for _, word := range words {
		if strings.TrimSpace(word) == "" {
			return nil, errs.New(errs.CodeInvalidArgument, "热词不能为空或全为空白")
		}
	}

	client := voiceHotwordClient(ctx)
	results := make([]any, 0, len(words))
	var batchErr *errs.Error
	for i, word := range words {
		failure := client.Mutate(cmd.Context(), action, word)
		if failure == nil {
			results = append(results, map[string]any{"text": word, "status": "ok"})
			continue
		}
		batchErr = failure
		results = append(results, map[string]any{"text": word, "status": "error", "error": failure.Payload()})
		if failure.Code == errs.CodeInvalidArgument || failure.Code == errs.CodeAlreadyExists {
			continue
		}
		for _, pending := range words[i+1:] {
			results = append(results, map[string]any{"text": pending, "status": "not_attempted"})
		}
		break
	}
	if batchErr != nil {
		batchErr.Details["results"] = results
		return nil, batchErr
	}
	if ctx.Format == output.NDJSON {
		return results, nil
	}
	return map[string]any{"ok": true, "action": action, "results": results}, nil
}

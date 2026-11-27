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

const hotwordWeights = `权重表示语音识别遇到相近读音时优先选用该词的程度。取值为 1 到 5 的整数。
  1–2  轻微偏好，适合偶尔使用、容易与普通词混淆的词。
  3–4  较强偏好，常用的人名、产品名和专业词通常用 4。
  5    最强偏好，适合希望优先识别为该专名的同音词。`

const hotwordBatchHelp = `多个词用空格分隔，含空格的词用引号括起。CLI 按输入顺序逐词提交。
调权时找不到词（404）、词条冲突（409）或校验失败（422）时继续处理后续词。鉴权、网络或服务异常时停止。
已写入的词会保留。部分失败时退出码非零，结果按词标记 ok（成功）、error（出错）或 not_attempted（未执行）。`

func newVoiceHotwordsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "hotwords",
		Short: "管理当前账号的云端语音识别热词",
		Long:  "查询、添加、调权或删除当前账号的云端语音识别热词。用 yoooclaw env show 查看环境，使用 yoooclaw auth 配置的默认 API Key。\n\n" + hotwordWeights + "\n新增词默认权重为 4。调整已有词的权重时，须用 --weight 指定新值。\n\n" + hotwordBatchHelp,
		Example: `  yoooclaw voice hotwords list --format json
  yoooclaw voice hotwords add "星桥" "YoooClaw Capture"
  yoooclaw voice hotwords update "星桥" "YoooClaw Capture" --weight 5
  yoooclaw voice hotwords delete "星桥" "YoooClaw Capture"`,
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "按页查看有效热词及其权重",
		Args:  cobra.NoArgs,
		RunE:  run(voiceHotwordList),
		Long:  "每次读取一页有效热词。JSON 输出的 items 包含词文本 text 和权重 weight。\n有下一页时，将 next_cursor 原样传给 --cursor。末页省略 next_cursor。",
		Example: `  yoooclaw voice hotwords list --limit 20 --format json
  yoooclaw voice hotwords list --limit 20 --cursor 42 --format json`,
	}
	list.Flags().Int("limit", 50, "每页词数，范围 1 到 100")
	list.Flags().Int64("cursor", 0, "上一页返回的 next_cursor，首次查询可省略")
	add := &cobra.Command{
		Use:   "add <词> [词...]",
		Short: "添加一个或多个热词",
		Args:  cobra.MinimumNArgs(1),
		RunE:  run(voiceHotwordMutate),
		Long:  "新增的词使用同一权重，默认 4。重复添加会保留已有权重。用 update 调整已有词的权重。\n\n" + hotwordWeights + "\n\n" + hotwordBatchHelp,
		Example: `  yoooclaw voice hotwords add "星桥" "YoooClaw Capture"
  yoooclaw voice hotwords add "云舟" --weight 5`,
	}
	add.Flags().Int("weight", 4, "本次新增热词的识别权重（1–2 轻微，3–4 较强，5 最强）")
	update := &cobra.Command{
		Use:     "update <词> [词...] --weight <1-5>",
		Short:   "调整一个或多个已有热词的识别权重",
		Args:    cobra.MinimumNArgs(1),
		RunE:    run(voiceHotwordMutate),
		Long:    "用 --weight 指定这批已有热词的新权重。词文本保持原样。找不到的词按失败返回。\n\n" + hotwordWeights + "\n\n" + hotwordBatchHelp,
		Example: `  yoooclaw voice hotwords update "星桥" "YoooClaw Capture" --weight 5`,
	}
	update.Flags().Int("weight", 0, "必填，所有指定热词的新权重（1–2 轻微，3–4 较强，5 最强）")
	del := &cobra.Command{
		Use:     "delete <词> [词...]",
		Aliases: []string{"remove"},
		Short:   "删除一个或多个热词",
		Args:    cobra.MinimumNArgs(1),
		RunE:    run(voiceHotwordMutate),
		Long:    "按提交的词文本删除当前账号的云端词条。词条已不存在时也返回成功。\n\n" + hotwordBatchHelp,
		Example: `  yoooclaw voice hotwords delete "星桥" "YoooClaw Capture"`,
	}
	group.AddCommand(list, add, update, del)
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
		rows = append(rows, map[string]any{"text": word.Text, "weight": word.Weight})
	}
	if ctx.Format == output.NDJSON {
		return rows, nil
	}
	result := map[string]any{"ok": true, "items": rows}
	if page.NextCursor != nil {
		result["next_cursor"] = *page.NextCursor
	}
	return result, nil
}

func voiceHotwordMutate(ctx *clictx.Context, cmd *cobra.Command, words []string) (any, error) {
	action := cmd.Name()
	var weight int
	if action == "update" && !cmd.Flags().Changed("weight") {
		return nil, errs.New(errs.CodeInvalidArgument, "调权须用 --weight 指定 1 到 5 的整数")
	}
	if action == "add" || action == "update" {
		weight, _ = cmd.Flags().GetInt("weight")
		if weight < 1 || weight > 5 {
			return nil, errs.New(errs.CodeInvalidArgument, "--weight 须为 1 到 5 的整数")
		}
	}
	for _, word := range words {
		if strings.TrimSpace(word) == "" {
			return nil, errs.New(errs.CodeInvalidArgument, "热词不能为空或全为空白")
		}
	}

	client := voiceHotwordClient(ctx)
	results := make([]any, 0, len(words))
	var batchErr *errs.Error
	for i, word := range words {
		failure := client.Mutate(cmd.Context(), action, word, weight)
		if failure == nil {
			results = append(results, map[string]any{"text": word, "status": "ok"})
			continue
		}
		batchErr = failure
		results = append(results, map[string]any{"text": word, "status": "error", "error": failure.Payload()})
		if failure.Code == errs.CodeInvalidArgument || failure.Code == errs.CodeAlreadyExists || failure.Code == errs.CodeNotFound {
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

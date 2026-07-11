package cli

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/light"
	"github.com/YoooClaw/cli/internal/lightrule"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

var notificationRuleSignalRE = regexp.MustCompile(`(?i)通知|消息|推送|来电|短信|邮件|微信|飞书|钉钉|notification|message|push|incoming\s+call|sms|e-?mail`)

// ── light ──

func newLightCmd() *cobra.Command {
	c := &cobra.Command{Use: "light", Short: "灯效硬件控制 🟡"}

	send := &cobra.Command{Use: "send", Short: "发送灯效指令到硬件（--segments / --preset / --rule 三选一）", Args: cobra.NoArgs, RunE: run(lightSend)}
	send.Flags().String("segments", "", "灯效参数 JSON（原始段）")
	send.Flags().String("preset", "", "内置预设 id（如 red-steady / red-strobe-3）")
	send.Flags().String("rule", "", "已保存的 lightrule 名")
	send.Flags().Bool("repeat", false, "无限循环播放（覆盖来源默认值）")
	send.Flags().String("repeat-times", "", "整条组合重复次数（0=无限，覆盖来源默认值）")
	send.Flags().String("reason", "", "本次亮灯原因")
	send.Flags().String("title", "", "本次亮灯标题")
	send.Flags().String("biz-unique-id", "", "调用方幂等标识")

	blink := &cobra.Command{Use: "+blink", Short: "灯效连通性测试（red-strobe-3）", Args: cobra.NoArgs, RunE: run(lightBlink)}

	c.AddCommand(send, blink)
	return c
}

func lightSend(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	segments, preset, rule := flagStr(cmd, "segments"), flagStr(cmd, "preset"), flagStr(cmd, "rule")
	if segments == "" && preset == "" && rule == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "需要 --segments / --preset / --rule 之一")
	}
	body := map[string]any{}
	if segments != "" {
		v, err := parseJSONArg(segments, "--segments")
		if err != nil {
			return nil, err
		}
		body["segments"] = v
	}
	if preset != "" {
		body["preset"] = preset
	}
	if rule != "" {
		body["rule"] = rule
	}
	if flagBool(cmd, "repeat") {
		body["repeat"] = true
	}
	if rt := flagStr(cmd, "repeat-times"); rt != "" {
		n, err := strconv.ParseFloat(rt, 64)
		if err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "--repeat-times 必须是数字")
		}
		body["repeat_times"] = n
	}
	if reason := flagStr(cmd, "reason"); reason != "" {
		body["reason"] = reason
	}
	if title := flagStr(cmd, "title"); title != "" {
		body["title"] = title
	}
	if bizUniqueID := flagStr(cmd, "biz-unique-id"); bizUniqueID != "" {
		body["bizUniqueId"] = bizUniqueID
	}
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, resBody, err := c.Request("POST", "/light/send", body)
	return resBody, err
}

func lightBlink(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, resBody, err := c.Request("POST", "/light/send", map[string]any{"preset": "red-strobe-3"})
	return resBody, err
}

// ── lightrule ──

func newLightruleCmd() *cobra.Command {
	c := &cobra.Command{Use: "lightrule", Short: "灯效规则管理 🟡"}

	list := &cobra.Command{Use: "list", Short: "列出所有规则及状态", Args: cobra.NoArgs, RunE: run(lightruleList)}
	show := &cobra.Command{Use: "show <id>", Short: "查看单条规则详情", Args: cobra.ExactArgs(1), RunE: run(lightruleShow)}

	create := &cobra.Command{Use: "create", Short: "通过云端 Agent 创建规则（--rule-text / --from-file）", Args: cobra.NoArgs, RunE: run(lightruleCreate)}
	addRuleFlags(create)
	update := &cobra.Command{Use: "update <id>", Short: "更新现有规则", Args: cobra.ExactArgs(1), RunE: run(lightruleUpdate)}
	addRuleFlags(update)

	del := &cobra.Command{Use: "delete <id>", Short: "删除规则（--yes）", Args: cobra.ExactArgs(1), RunE: run(lightruleDelete)}
	del.Flags().Bool("yes", false, "跳过确认")
	enable := &cobra.Command{Use: "enable <id>", Short: "启用单条规则", Args: cobra.ExactArgs(1), RunE: run(lightruleEnable)}
	disable := &cobra.Command{Use: "disable <id>", Short: "停用单条规则", Args: cobra.ExactArgs(1), RunE: run(lightruleDisable)}

	on := &cobra.Command{Use: "+on", Short: "启用所有规则", Args: cobra.NoArgs, RunE: run(lightruleOn)}
	off := &cobra.Command{Use: "+off", Short: "停用所有规则（保留定义）", Args: cobra.NoArgs, RunE: run(lightruleOff)}

	c.AddCommand(list, show, create, update, del, enable, disable, on, off)
	return c
}

func addRuleFlags(c *cobra.Command) {
	c.Flags().String("from-file", "", "从 JSON 读 ruleText 或更新 patch（- 为 stdin）")
	c.Flags().String("rule-text", "", "用户自然语言灯效规则，由云端 Agent 编译")
	c.Flags().String("intent", "", "兼容别名：等同 --rule-text")
	c.Flags().String("title", "", "展示名（仅 update）")
	c.Flags().String("description", "", "触发描述（仅 update）")
	c.Flags().String("segments", "", "灯效段 JSON（仅 update）")
	c.Flags().String("light-action", "", "兼容别名：灯效段 JSON（仅 update）")
	c.Flags().String("repeat-times", "", "重复次数 0/1（仅 update）")
	c.Flags().String("enabled", "", "true/false（仅 update）")
}

func cloudLightRuleClient() (*lightrule.CloudClient, error) {
	return lightrule.NewCloudClient()
}

func lightruleList(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	_ = ctx
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	rules, err := c.List()
	if err != nil {
		return nil, cloudRuleError(err)
	}
	return map[string]any{"ok": true, "rules": rules}, nil
}

func lightruleShow(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	_ = ctx
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	rules, err := c.List()
	if err != nil {
		return nil, cloudRuleError(err)
	}
	id := args[0]
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok && (m["id"] == id || m["name"] == id) {
			return map[string]any{"ok": true, "rule": m}, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "规则不存在："+id)
}

func buildRuleParams(cmd *cobra.Command) (map[string]any, error) {
	if ff := flagStr(cmd, "from-file"); ff != "" {
		var raw string
		if ff == "-" {
			s, err := prompt.ReadStdin()
			if err != nil {
				return nil, err
			}
			raw = s
		} else {
			b, err := os.ReadFile(ff)
			if err != nil {
				return nil, errs.New(errs.CodeInvalidArgument, "无法读取文件："+ff)
			}
			raw = string(b)
		}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "--from-file 内容不是合法 JSON 对象")
		}
		return m, nil
	}

	params := map[string]any{}
	if ruleText := firstNonEmptyStr(flagStr(cmd, "rule-text"), flagStr(cmd, "intent")); ruleText != "" {
		params["ruleText"] = ruleText
	}
	if title := flagStr(cmd, "title"); title != "" {
		params["title"] = title
	}
	if description := flagStr(cmd, "description"); description != "" {
		params["description"] = description
	}
	segmentsJSON := firstNonEmptyStr(flagStr(cmd, "segments"), flagStr(cmd, "light-action"))
	if segmentsJSON != "" {
		action, err := parseJSONArg(segmentsJSON, "--segments")
		if err != nil {
			return nil, err
		}
		params["segments"] = segmentsFromAction(action)
	}
	if rt := flagStr(cmd, "repeat-times"); rt != "" {
		repeatTimes, err := strconv.ParseFloat(rt, 64)
		if err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "--repeat-times 必须是数字")
		}
		params["repeat_times"] = repeatTimes
	}
	if enabled := flagStr(cmd, "enabled"); enabled != "" {
		value, err := strconv.ParseBool(enabled)
		if err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "--enabled 必须是 true 或 false")
		}
		params["enabled"] = value
	}
	return params, nil
}

// segmentsFromAction 对齐 TS：数组直接用；对象取 .segments，缺省回退整体。
func segmentsFromAction(action any) any {
	if _, ok := action.([]any); ok {
		return action
	}
	if obj, ok := action.(map[string]any); ok {
		if segs, ok := obj["segments"]; ok {
			return segs
		}
	}
	return action
}

func lightruleCreate(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	_ = ctx
	params, err := buildRuleParams(cmd)
	if err != nil {
		return nil, err
	}
	ruleText, _ := params["ruleText"].(string)
	if strings.TrimSpace(ruleText) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "ruleText is required；请使用 --rule-text 或 --from-file '{\"ruleText\":\"...\"}'")
	}
	if len(params) != 1 {
		return nil, errs.New(errs.CodeInvalidArgument, "create only accepts ruleText")
	}
	if !notificationRuleSignalRE.MatchString(ruleText) {
		return nil, errs.New(errs.CodeInvalidArgument, "持久灯效规则必须包含通知、消息、来电等触发条件；一次性亮灯请使用 light send")
	}
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	result, err := c.Create(ruleText)
	if err != nil {
		return nil, cloudRuleError(err)
	}
	result["ok"] = true
	return result, nil
}

func lightruleUpdate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	_ = ctx
	params, err := buildRuleParams(cmd)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"id", "name", "ruleId", "ruleName"} {
		delete(params, key)
	}
	if err := validateCloudRulePatch(params); err != nil {
		return nil, err
	}
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	result, err := c.Update(args[0], params)
	if err != nil {
		return nil, cloudRuleError(err)
	}
	result["ok"] = true
	result["updated"] = true
	return result, nil
}

func lightruleDelete(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	if !flagBool(cmd, "yes") {
		ok, err := prompt.Confirm("确认删除规则 `"+args[0]+"`？", false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.New(errs.CodeConfirmationRequired, "已取消")
		}
	}
	_ = ctx
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	result, err := c.Delete(args[0])
	if err != nil {
		return nil, cloudRuleError(err)
	}
	result["ok"] = true
	result["deleted"] = true
	return result, nil
}

func lightruleEnable(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return lightruleSetEnabled(ctx, args[0], true)
}

func lightruleDisable(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return lightruleSetEnabled(ctx, args[0], false)
}

func lightruleSetEnabled(ctx *clictx.Context, id string, enabled bool) (any, error) {
	_ = ctx
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	result, err := c.Update(id, map[string]any{"enabled": enabled})
	if err != nil {
		return nil, cloudRuleError(err)
	}
	result["ok"] = true
	result["updated"] = true
	return result, nil
}

func lightruleOn(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightruleToggleAll(ctx, true)
}

func lightruleOff(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightruleToggleAll(ctx, false)
}

func lightruleToggleAll(ctx *clictx.Context, enabled bool) (any, error) {
	_ = ctx
	c, err := cloudLightRuleClient()
	if err != nil {
		return nil, err
	}
	rules, err := c.List()
	if err != nil {
		return nil, cloudRuleError(err)
	}
	results := make([]any, 0, len(rules))
	for _, r := range rules {
		m, _ := r.(map[string]any)
		identifier := firstNonEmptyStr(stringAny(m["id"]), stringAny(m["name"]))
		_, updateErr := c.Update(identifier, map[string]any{"enabled": enabled})
		results = append(results, map[string]any{"id": identifier, "name": m["name"], "ok": updateErr == nil})
	}
	return map[string]any{"ok": true, "enabled": enabled, "count": len(results), "results": results}, nil
}

func cloudRuleError(err error) error {
	remote, ok := err.(*lightrule.RemoteError)
	if !ok {
		return err
	}
	switch remote.Status {
	case 401, 403:
		return errs.New(errs.CodeUnauthorized, remote.Message)
	case 404:
		return errs.New(errs.CodeNotFound, remote.Message)
	}
	return errs.New(remote.Code, remote.Message, map[string]any{"status": remote.Status})
}

func validateCloudRulePatch(params map[string]any) error {
	allowed := map[string]bool{
		"ruleText": true, "title": true, "description": true, "enabled": true,
		"segments": true, "repeat": true, "repeat_times": true,
	}
	for key := range params {
		if !allowed[key] {
			return errs.New(errs.CodeInvalidArgument, "unsupported update field: "+key)
		}
	}
	if len(params) == 0 {
		return errs.New(errs.CodeInvalidArgument, "at least one update field is required")
	}
	if ruleText, hasRuleText := params["ruleText"]; hasRuleText {
		text, ok := ruleText.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return errs.New(errs.CodeInvalidArgument, "ruleText must be a non-empty string")
		}
		if len(params) != 1 {
			return errs.New(errs.CodeInvalidArgument, "ruleText cannot be mixed with title/enabled/description/segments/repeat_times")
		}
		return nil
	}
	if title, ok := params["title"]; ok {
		text, valid := title.(string)
		if !valid || strings.TrimSpace(text) == "" {
			return errs.New(errs.CodeInvalidArgument, "title must be a non-empty string")
		}
	}
	if description, ok := params["description"]; ok {
		if _, valid := description.(string); !valid {
			return errs.New(errs.CodeInvalidArgument, "description must be a string")
		}
	}
	if enabled, ok := params["enabled"]; ok {
		if _, valid := enabled.(bool); !valid {
			return errs.New(errs.CodeInvalidArgument, "enabled must be a boolean")
		}
	}
	if segments, ok := params["segments"]; ok {
		validation := light.ValidateSegments(segments)
		if !validation.Valid {
			return errs.New(errs.CodeInvalidArgument, validationErrorsText(validation))
		}
		params["segments"] = validation.Segments
	}
	if repeat, ok := params["repeat"]; ok {
		if _, valid := repeat.(bool); !valid {
			return errs.New(errs.CodeInvalidArgument, "repeat must be a boolean")
		}
	}
	if _, hasRepeat := params["repeat"]; hasRepeat || params["repeat_times"] != nil {
		repeatTimes, err := light.NormalizeRepeatTimes(light.RepeatInputFromAny(params["repeat"], params["repeat_times"]))
		if err != nil {
			return errs.New(errs.CodeInvalidArgument, err.Error())
		}
		if err := light.AssertAncsRepeatTimes(repeatTimes); err != nil {
			return errs.New(errs.CodeInvalidArgument, err.Error())
		}
		params["repeat_times"] = repeatTimes
		delete(params, "repeat")
	}
	return nil
}

func validationErrorsText(result light.ValidationResult) string {
	parts := make([]string, 0, len(result.Errors))
	for _, item := range result.Errors {
		parts = append(parts, item.Field+": "+item.Message)
	}
	return strings.Join(parts, "; ")
}

func stringAny(value any) string {
	text, _ := value.(string)
	return text
}

func firstNonEmptyStr(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

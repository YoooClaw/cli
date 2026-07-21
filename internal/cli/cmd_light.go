package cli

import (
	"encoding/json"
	"strconv"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/light"
	"github.com/YoooClaw/cli/internal/lightgw"
	"github.com/YoooClaw/cli/internal/lightrule"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

// 灯效命令与 openclaw plugin 的 Agent 工具同构，全部触发云端接口：
// light send 打灯效下发 API，lightrule CRUD 打 Notification Intelligence
// Service 的插件侧规则 API（见 internal/lightrule/client.go）。
// 仅 +gateway（hermes 插件桥）保留本地规则文件形态，与 plugin 的
// gateway 方法 / daemon HTTP 逐字节同构。

// ── light ──

func newLightCmd() *cobra.Command {
	c := &cobra.Command{Use: "light", Short: "灯效硬件控制（云端下发）"}

	send := &cobra.Command{Use: "send", Short: "发送灯效指令到硬件（--segments / --preset / --rule 三选一）", Args: cobra.NoArgs, RunE: run(lightSend)}
	send.Flags().String("segments", "", "灯效参数 JSON（原始段）")
	send.Flags().String("preset", "", "内置预设 id（如 red-steady / red-strobe-3）")
	send.Flags().String("rule", "", "已保存的 lightrule 名")
	send.Flags().Bool("repeat", false, "无限循环播放（覆盖来源默认值）")
	send.Flags().String("repeat-times", "", "整条组合重复次数（0=无限，覆盖来源默认值）")

	blink := &cobra.Command{Use: "+blink", Short: "灯效连通性测试（red-strobe-3）", Args: cobra.NoArgs, RunE: run(lightBlink)}

	// （内部）hermes 插件桥的 gateway 入口：stdin 读参数 JSON，输出
	// {"status":<http 状态码>,"body":<daemon HTTP 同构响应体>} envelope。
	gateway := &cobra.Command{Use: "+gateway <method>", Hidden: true, Args: cobra.ExactArgs(1), RunE: run(lightGateway)}

	c.AddCommand(send, blink, gateway)
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
		// 规则存在云端，先解析成 segments 再下发（本地规则文件仅供 +gateway 兼容路径）。
		resolved, err := lightruleFind(ctx, rule)
		if err != nil {
			return nil, err
		}
		body["segments"] = resolved["segments"]
		if !flagBool(cmd, "repeat") && flagStr(cmd, "repeat-times") == "" {
			body["repeat_times"] = resolved["repeat_times"]
		}
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
	return lightSendDirect(ctx, body)
}

func lightBlink(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightSendDirect(ctx, map[string]any{"preset": "red-strobe-3"})
}

// cloudHost 解析当前 profile 的云端主机；profile 还没 init（config 读不到）时按
// 零值走环境默认值，保证 light/lightrule 在裸装状态下仍可用。
func cloudHost(ctx *clictx.Context) string {
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return config.ResolveCloudHost(config.Config{})
	}
	return config.ResolveCloudHost(cfg)
}

func lightSendDirect(ctx *clictx.Context, body map[string]any) (any, error) {
	result, gerr := lightgw.Send(ctx.Paths.LightRules, body, cloudHost(ctx), creds.ResolveAPIKey().Value, noopLightLogger{})
	if gerr != nil {
		return nil, errs.New(gerr.Code, gerr.Message)
	}
	return result, nil
}

// lightGateway 是插件桥的子进程入口：与 daemon 对同一 method 的 HTTP 响应
// 逐字节同构（status + body），插件侧零适配转发。
func lightGateway(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	raw, err := prompt.ReadStdin()
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if raw != "" {
		if json.Unmarshal([]byte(raw), &body) != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "stdin 参数不是合法 JSON 对象")
		}
	}
	method := args[0]
	if method == "light.send" {
		result, gerr := lightgw.Send(ctx.Paths.LightRules, body, cloudHost(ctx), creds.ResolveAPIKey().Value, noopLightLogger{})
		if gerr != nil {
			status := gerr.Status
			if status == 0 {
				status = 400
			}
			return gatewayEnvelope(status, gatewayErrBody(gerr)), nil
		}
		return gatewayEnvelope(200, result), nil
	}
	payload, gerr := lightgw.Rules(ctx.Paths.LightRules, method, body)
	if gerr != nil {
		if gerr.Status == 404 {
			return gatewayEnvelope(404, gatewayErrBody(gerr)), nil
		}
		return gatewayEnvelope(200, gatewayErrBody(gerr)), nil
	}
	return gatewayEnvelope(200, map[string]any{"ok": true, "data": payload}), nil
}

func gatewayEnvelope(status int, body any) map[string]any {
	return map[string]any{"ok": true, "status": status, "body": body}
}

func gatewayErrBody(gerr *lightgw.Err) map[string]any {
	return map[string]any{"ok": false, "error": map[string]any{"code": gerr.Code, "message": gerr.Message}}
}

type noopLightLogger struct{}

func (noopLightLogger) Info(string) {}
func (noopLightLogger) Warn(string) {}

// ── lightrule（云端 Notification Intelligence Service）──

func newLightruleCmd() *cobra.Command {
	c := &cobra.Command{Use: "lightrule", Short: "灯效规则管理（云端）"}

	list := &cobra.Command{Use: "list", Short: "列出云端所有规则及状态", Args: cobra.NoArgs, RunE: run(lightruleList)}
	show := &cobra.Command{Use: "show <id>", Short: "查看单条规则详情", Args: cobra.ExactArgs(1), RunE: run(lightruleShow)}

	create := &cobra.Command{Use: "create", Short: "创建规则（--intent 自然语言，云端 Agent 编译）", Args: cobra.NoArgs, RunE: run(lightruleCreate)}
	create.Flags().String("intent", "", "自然语言规则，例如“老板发微信时红灯快闪”")
	update := &cobra.Command{Use: "update <id>", Short: "更新规则（--intent 重编译，或 --title/--description/--segments/--repeat-times 局部更新）", Args: cobra.ExactArgs(1), RunE: run(lightruleUpdate)}
	update.Flags().String("intent", "", "新的自然语言规则，传入后由云端 Agent 重编译（不能与其他字段混用）")
	update.Flags().String("title", "", "新的展示名/短标题")
	update.Flags().String("description", "", "新的触发条件描述")
	update.Flags().String("segments", "", "新的灯效段序列 JSON")
	update.Flags().Bool("repeat", false, "无限循环播放")
	update.Flags().String("repeat-times", "", "重复次数（0=无限，1=一轮）")

	del := &cobra.Command{Use: "delete <id>", Short: "删除规则（--yes）", Args: cobra.ExactArgs(1), RunE: run(lightruleDelete)}
	del.Flags().Bool("yes", false, "跳过确认")
	enable := &cobra.Command{Use: "enable <id>", Short: "启用单条规则", Args: cobra.ExactArgs(1), RunE: run(lightruleEnable)}
	disable := &cobra.Command{Use: "disable <id>", Short: "停用单条规则", Args: cobra.ExactArgs(1), RunE: run(lightruleDisable)}

	on := &cobra.Command{Use: "+on", Short: "启用所有规则", Args: cobra.NoArgs, RunE: run(lightruleOn)}
	off := &cobra.Command{Use: "+off", Short: "停用所有规则（保留定义）", Args: cobra.NoArgs, RunE: run(lightruleOff)}

	c.AddCommand(list, show, create, update, del, enable, disable, on, off)
	return c
}

func lightruleClient(ctx *clictx.Context) *lightrule.Client {
	return &lightrule.Client{APIKey: creds.ResolveAPIKey().Value, Host: cloudHost(ctx), Logger: noopLightLogger{}}
}

// lightruleErr 把云端 APIError 转成 CLI 结构化错误（code 原样透传）。
func lightruleErr(err error) error {
	if e, ok := err.(*lightrule.APIError); ok {
		return errs.New(e.Code, e.Message)
	}
	return err
}

// lightruleFind 按 id/name 从云端解析单条规则。
func lightruleFind(ctx *clictx.Context, id string) (map[string]any, error) {
	rules, err := lightruleClient(ctx).List()
	if err != nil {
		return nil, lightruleErr(err)
	}
	for _, rule := range rules {
		if rule["id"] == id || rule["name"] == id {
			return rule, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "灯效规则不存在："+id)
}

func lightruleList(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	rules, err := lightruleClient(ctx).List()
	if err != nil {
		return nil, lightruleErr(err)
	}
	return map[string]any{"ok": true, "rules": rules}, nil
}

func lightruleShow(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	rule, err := lightruleFind(ctx, args[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "rule": rule}, nil
}

func lightruleCreate(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	intent := flagStr(cmd, "intent")
	if intent == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "需要 --intent（自然语言规则，云端 Agent 编译）")
	}
	result, err := lightruleClient(ctx).Create(intent)
	if err != nil {
		return nil, lightruleErr(err)
	}
	result["ok"] = true
	return result, nil
}

// buildRulePatch 组装云端 PATCH 体：--intent（ruleText 重编译）与普通字段互斥；
// segments 本地先过 light.ValidateSegments，repeat/repeat-times 归一化成 repeat_times。
func buildRulePatch(cmd *cobra.Command) (map[string]any, error) {
	patch := map[string]any{}
	if title := flagStr(cmd, "title"); title != "" {
		patch["title"] = title
	}
	if desc := flagStr(cmd, "description"); desc != "" {
		patch["description"] = desc
	}
	if segs := flagStr(cmd, "segments"); segs != "" {
		v, err := parseJSONArg(segs, "--segments")
		if err != nil {
			return nil, err
		}
		res := light.ValidateSegments(v)
		if !res.Valid {
			data, _ := json.Marshal(res.Errors)
			return nil, errs.New("VALIDATION_FAILED", string(data))
		}
		patch["segments"] = res.Segments
	}
	if flagBool(cmd, "repeat") || flagStr(cmd, "repeat-times") != "" {
		var repeatTimes any
		if rt := flagStr(cmd, "repeat-times"); rt != "" {
			n, err := strconv.ParseFloat(rt, 64)
			if err != nil {
				return nil, errs.New(errs.CodeInvalidArgument, "--repeat-times 必须是数字")
			}
			repeatTimes = n
		}
		var repeat any
		if flagBool(cmd, "repeat") {
			repeat = true
		}
		normalized, err := light.NormalizeRepeatTimes(light.RepeatInputFromAny(repeat, repeatTimes))
		if err == nil {
			err = light.AssertAncsRepeatTimes(normalized)
		}
		if err != nil {
			return nil, errs.New("VALIDATION_FAILED", err.Error())
		}
		patch["repeat_times"] = float64(normalized)
	}
	if intent := flagStr(cmd, "intent"); intent != "" {
		if len(patch) > 0 {
			return nil, errs.New(errs.CodeInvalidArgument, "--intent 不能与 --title/--description/--segments/--repeat-times 混用")
		}
		patch["ruleText"] = intent
	}
	if len(patch) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "至少提供一个更新字段")
	}
	return patch, nil
}

func lightruleUpdate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	patch, err := buildRulePatch(cmd)
	if err != nil {
		return nil, err
	}
	result, err := lightruleClient(ctx).Update(args[0], patch)
	if err != nil {
		return nil, lightruleErr(err)
	}
	result["ok"] = true
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
	result, err := lightruleClient(ctx).Delete(args[0])
	if err != nil {
		return nil, lightruleErr(err)
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
	result, err := lightruleClient(ctx).Update(id, map[string]any{"enabled": enabled})
	if err != nil {
		return nil, lightruleErr(err)
	}
	result["ok"] = true
	return result, nil
}

func lightruleOn(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightruleToggleAll(ctx, true)
}

func lightruleOff(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightruleToggleAll(ctx, false)
}

func lightruleToggleAll(ctx *clictx.Context, enabled bool) (any, error) {
	client := lightruleClient(ctx)
	rules, err := client.List()
	if err != nil {
		return nil, lightruleErr(err)
	}
	results := make([]any, 0, len(rules))
	for _, rule := range rules {
		id, _ := rule["id"].(string)
		_, updateErr := client.Update(id, map[string]any{"enabled": enabled})
		results = append(results, map[string]any{"id": id, "name": rule["name"], "ok": updateErr == nil})
	}
	return map[string]any{"ok": true, "enabled": enabled, "count": len(results), "results": results}, nil
}

package cli

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

// ── light ──

func newLightCmd() *cobra.Command {
	c := &cobra.Command{Use: "light", Short: "灯效硬件控制 🟡"}

	send := &cobra.Command{Use: "send", Short: "发送灯效指令到硬件（--segments / --preset / --rule 三选一）", Args: cobra.NoArgs, RunE: run(lightSend)}
	send.Flags().String("segments", "", "灯效参数 JSON（原始段）")
	send.Flags().String("preset", "", "内置预设 id（如 red-steady / red-strobe-3）")
	send.Flags().String("rule", "", "已保存的 lightrule 名")
	send.Flags().Bool("repeat", false, "无限循环播放（覆盖来源默认值）")
	send.Flags().String("repeat-times", "", "整条组合重复次数（0=无限，覆盖来源默认值）")

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

	create := &cobra.Command{Use: "create", Short: "创建规则（--from-file / --intent ...）", Args: cobra.NoArgs, RunE: run(lightruleCreate)}
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
	c.Flags().String("from-file", "", "从 JSON 读规则（- 为 stdin）")
	c.Flags().String("name", "", "规则名")
	c.Flags().String("intent", "", "自然语言意图描述")
	c.Flags().String("light-action", "", "命中后的 light 动作 JSON")
	c.Flags().String("match-rules", "", "前置硬过滤规则 JSON")
}

func lightruleListRules(c *daemon.Client) ([]any, error) {
	_, body, err := c.Request("POST", "/gateway/lightrules.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	if m, ok := body.(map[string]any); ok {
		if data, ok := m["data"].(map[string]any); ok {
			if rules, ok := data["rules"].([]any); ok {
				return rules, nil
			}
		}
	}
	return []any{}, nil
}

func lightruleList(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := lightruleListRules(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "rules": rules}, nil
}

func lightruleShow(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := lightruleListRules(c)
	if err != nil {
		return nil, err
	}
	id := args[0]
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok && (m["id"] == id || m["name"] == id) {
			return map[string]any{"ok": true, "rule": m}, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "规则不存在："+id)
}

func buildRuleParams(cmd *cobra.Command, name string) (map[string]any, error) {
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
	if name != "" {
		params["name"] = name
	}
	if n := flagStr(cmd, "name"); n != "" {
		params["name"] = n
	}
	if intent := flagStr(cmd, "intent"); intent != "" {
		params["description"] = intent
	}
	if la := flagStr(cmd, "light-action"); la != "" {
		action, err := parseJSONArg(la, "--light-action")
		if err != nil {
			return nil, err
		}
		params["segments"] = segmentsFromAction(action)
	}
	if mr := flagStr(cmd, "match-rules"); mr != "" {
		match, err := parseJSONArg(mr, "--match-rules")
		if err != nil {
			return nil, err
		}
		params["matchRules"] = match
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
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	params, err := buildRuleParams(cmd, "")
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("POST", "/gateway/lightrules.create", params)
	if err != nil {
		return nil, err
	}
	if m, ok := body.(map[string]any); !ok || m["ok"] == false {
		return nil, errs.New(errs.CodeInvalidArgument, createErrorMessage(body))
	}
	return dataOrBody(body), nil
}

func lightruleUpdate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	params, err := buildRuleParams(cmd, args[0])
	if err != nil {
		return nil, err
	}
	params["name"] = args[0]
	_, body, err := c.Request("POST", "/gateway/lightrules.update", params)
	if err != nil {
		return nil, err
	}
	return dataOrBody(body), nil
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
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("POST", "/gateway/lightrules.delete", map[string]any{"name": args[0]})
	if err != nil {
		return nil, err
	}
	return dataOrBody(body), nil
}

func lightruleEnable(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return lightruleSetEnabled(ctx, args[0], true)
}

func lightruleDisable(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return lightruleSetEnabled(ctx, args[0], false)
}

func lightruleSetEnabled(ctx *clictx.Context, id string, enabled bool) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("POST", "/gateway/lightrules.update", map[string]any{"name": id, "enabled": enabled})
	if err != nil {
		return nil, err
	}
	return dataOrBody(body), nil
}

func lightruleOn(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightruleToggleAll(ctx, true)
}

func lightruleOff(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return lightruleToggleAll(ctx, false)
}

func lightruleToggleAll(ctx *clictx.Context, enabled bool) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := lightruleListRules(c)
	if err != nil {
		return nil, err
	}
	results := make([]any, 0, len(rules))
	for _, r := range rules {
		m, _ := r.(map[string]any)
		name := m["name"]
		_, body, err := c.Request("POST", "/gateway/lightrules.update", map[string]any{"name": name, "enabled": enabled})
		ok := err == nil
		if bm, isMap := body.(map[string]any); isMap && bm["ok"] == false {
			ok = false
		}
		results = append(results, map[string]any{"name": name, "ok": ok})
	}
	return map[string]any{"ok": true, "enabled": enabled, "count": len(results), "results": results}, nil
}

// dataOrBody 返回 gateway 响应里的 data；缺省回退整个 body。
func dataOrBody(body any) any {
	if m, ok := body.(map[string]any); ok {
		if data, ok := m["data"]; ok {
			return data
		}
	}
	return body
}

func createErrorMessage(body any) string {
	if m, ok := body.(map[string]any); ok {
		if e, ok := m["error"]; ok {
			data, _ := json.Marshal(e)
			return string(data)
		}
	}
	data, _ := json.Marshal(body)
	return string(data)
}

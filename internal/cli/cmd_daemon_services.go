package cli

import (
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

// daemonProxy 是依赖 daemon 的命令前置：确保 daemon 在运行并返回 client。
func daemonProxy(ctx *clictx.Context) (*daemon.Client, error) {
	if err := daemon.AssertRunning(ctx.Paths); err != nil {
		return nil, err
	}
	return daemon.NewClient(ctx.Paths), nil
}

func parseJSONArg(raw, label string) (any, error) {
	if raw == "" {
		return nil, nil
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil, errs.New(errs.CodeInvalidArgument, label+" 不是合法 JSON")
	}
	return v, nil
}

// ── tunnel ──

func newTunnelCmd() *cobra.Command {
	c := &cobra.Command{Use: "tunnel", Short: "Relay 隧道 🟡"}
	status := &cobra.Command{Use: "status", Short: "查询 Relay 连接状态", Args: cobra.NoArgs, RunE: run(tunnelStatus)}
	status.Flags().String("client", "", "只看指定 clientLabel；all 为全部")
	reconnect := &cobra.Command{Use: "reconnect", Short: "强制重连", Args: cobra.NoArgs, RunE: run(tunnelReconnect)}
	reconnect.Flags().String("client", "", "只重连指定 clientLabel")
	test := &cobra.Command{Use: "+test", Short: "daemon 本地 ingest + 鉴权自检（echo 回环）", Args: cobra.NoArgs, RunE: run(tunnelTest)}
	test.Flags().String("client", "", "用指定 clientLabel 的 api-key 做回环写入")
	c.AddCommand(status, reconnect, test)
	return c
}

func tunnelStatus(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	path := "/tunnel/status"
	if client := flagStr(cmd, "client"); client != "" {
		path += "?client=" + url.QueryEscape(client)
	}
	_, body, err := c.Request("GET", path, nil)
	return body, err
}

func tunnelReconnect(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	var reqBody any
	if client := flagStr(cmd, "client"); client != "" {
		reqBody = map[string]any{"client": client}
	}
	_, body, err := c.Request("POST", "/tunnel/reconnect", reqBody)
	return body, err
}

func tunnelTest(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	var reqBody any
	if client := flagStr(cmd, "client"); client != "" {
		reqBody = map[string]any{"client": client}
	}
	_, body, err := c.Request("POST", "/tunnel/test", reqBody)
	return body, err
}

// ── monitor ──

func newMonitorCmd() *cobra.Command {
	c := &cobra.Command{Use: "monitor", Short: "定时通知监控任务 🟡"}
	list := &cobra.Command{Use: "list", Short: "列出所有监控任务", Args: cobra.NoArgs, RunE: run(monitorList)}
	show := &cobra.Command{Use: "show <name>", Short: "查看监控任务详情", Args: cobra.ExactArgs(1), RunE: run(monitorShow)}
	create := &cobra.Command{Use: "create <name>", Short: "创建监控任务（cron 驱动）", Args: cobra.ExactArgs(1), RunE: run(monitorCreate)}
	create.Flags().String("description", "", "任务描述（必填）")
	create.Flags().String("match-rules", "", "匹配规则 JSON（必填）")
	create.Flags().String("schedule", "", "cron 表达式（必填）")
	del := &cobra.Command{Use: "delete <name>", Short: "删除监控任务（--yes）", Args: cobra.ExactArgs(1), RunE: run(monitorDelete)}
	del.Flags().Bool("yes", false, "跳过确认")
	enable := &cobra.Command{Use: "enable <name>", Short: "启用监控任务", Args: cobra.ExactArgs(1), RunE: run(monitorEnable)}
	disable := &cobra.Command{Use: "disable <name>", Short: "暂停监控任务", Args: cobra.ExactArgs(1), RunE: run(monitorDisable)}
	c.AddCommand(list, show, create, del, enable, disable)
	return c
}

func monitorList(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("GET", "/monitors", nil)
	return body, err
}

func monitorShow(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("GET", "/monitors", nil)
	if err != nil {
		return nil, err
	}
	if m, ok := body.(map[string]any); ok {
		if list, ok := m["monitors"].([]any); ok {
			for _, item := range list {
				if mon, ok := item.(map[string]any); ok && mon["name"] == args[0] {
					return map[string]any{"ok": true, "monitor": mon}, nil
				}
			}
		}
	}
	return nil, errs.New(errs.CodeNotFound, "监控任务不存在："+args[0])
}

func monitorCreate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	desc, sched, mr := flagStr(cmd, "description"), flagStr(cmd, "schedule"), flagStr(cmd, "match-rules")
	if desc == "" || mr == "" || sched == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--description / --match-rules / --schedule 均必填")
	}
	matchRules, err := parseJSONArg(mr, "--match-rules")
	if err != nil {
		return nil, err
	}
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("POST", "/monitors", map[string]any{
		"name": args[0], "description": desc, "matchRules": matchRules, "schedule": sched,
	})
	return body, err
}

func monitorDelete(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	if !flagBool(cmd, "yes") {
		ok, err := prompt.Confirm("确认删除监控任务 `"+args[0]+"`？", false)
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
	_, body, err := c.Request("DELETE", "/monitors/"+url.PathEscape(args[0]), nil)
	return body, err
}

func monitorEnable(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return monitorToggle(ctx, args[0], "enable")
}

func monitorDisable(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	return monitorToggle(ctx, args[0], "disable")
}

func monitorToggle(ctx *clictx.Context, name, action string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := c.Request("POST", "/monitors/"+url.PathEscape(name)+"/"+action, nil)
	return body, err
}

// ── gateway test ──

func newGatewayCmd() *cobra.Command {
	c := &cobra.Command{Use: "gateway", Short: "协议自检 🟢/🟡"}
	test := &cobra.Command{Use: "test", Short: "模拟手机端调 daemon /notifications，验证本地连通与鉴权 🟡", Args: cobra.NoArgs, RunE: run(gatewayTest)}
	test.Flags().String("from-phone-ip", "", "模拟来源 IP")
	test.Flags().Bool("via-relay", false, "复用 tunnel +test 本地回环路径")
	c.AddCommand(test)
	return c
}

func gatewayTest(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	if flagBool(cmd, "via-relay") {
		_, body, err := c.Request("POST", "/tunnel/test", nil)
		return map[string]any{"ok": true, "viaRelay": true, "result": body}, err
	}
	probe := map[string]any{"notifications": []any{map[string]any{
		"id": "gwtest_" + strconv.FormatInt(time.Now().UnixMilli(), 10), "app": "yoooclaw.gatewaytest",
		"title": "gateway test", "body": "probe", "timestamp": time.Now().Format(time.RFC3339),
	}}}
	status, body, err := c.Request("POST", "/notifications", probe)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": status >= 200 && status < 300, "fromPhoneIp": nilIfEmpty(flagStr(cmd, "from-phone-ip")),
		"status": status, "response": body,
	}, nil
}

// ── raw api ──

func newAPICmd() *cobra.Command {
	c := &cobra.Command{Use: "api <method> <path>", Short: "Raw HTTP escape hatch 🟡", Args: cobra.ExactArgs(2), RunE: run(apiRaw)}
	c.Flags().String("data", "", "请求体（@file 读文件、- 读 stdin）")
	c.Flags().StringArray("header", nil, "追加 header（可重复）")
	return c
}

func apiRaw(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	method, path := strings.ToUpper(args[0]), args[1]
	var data any
	if d := flagStr(cmd, "data"); d != "" {
		var raw string
		switch {
		case d == "-":
			s, err := prompt.ReadStdin()
			if err != nil {
				return nil, err
			}
			raw = s
		case strings.HasPrefix(d, "@"):
			b, err := os.ReadFile(d[1:])
			if err != nil {
				return nil, errs.New(errs.CodeInvalidArgument, "无法读取文件："+d[1:])
			}
			raw = string(b)
		default:
			raw = d
		}
		if json.Unmarshal([]byte(raw), &data) != nil {
			data = raw
		}
	}
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	status, body, err := c.Request(method, path, data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": status >= 200 && status < 300, "status": status, "body": body}, nil
}

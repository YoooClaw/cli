package cli

import (
	"encoding/json"
	"os"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

// startDaemonForInit 在 init 收尾拉起 daemon；失败/已在运行都不阻断 init。
func startDaemonForInit(ctx *clictx.Context) map[string]any {
	if st := daemon.State(ctx.Paths); st.Running {
		return map[string]any{"started": true, "alreadyRunning": true, "pid": st.Lock.PID}
	}
	lock, err := daemon.Spawn(ctx, daemon.StartOpts{})
	if err != nil {
		return map[string]any{"started": false, "error": err.Error()}
	}
	return map[string]any{"started": true, "pid": lock.PID}
}

func initHint(started, apiKeyPresent bool) string {
	if !started {
		if apiKeyPresent {
			return "配置已就绪：运行 yc daemon start 启动 daemon"
		}
		return "配置已就绪：运行 yc auth set-api-key <ock_…> 设置 api-key，再 yc daemon start"
	}
	if apiKeyPresent {
		return "已就绪：daemon 已启动；手机 App 绑定同一账号即可收发（Relay 隧道 Phase 3 接入）"
	}
	return "daemon 已启动，但尚未设置 api-key：运行 yc auth set-api-key <ock_…> 后 yc daemon restart"
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "配置管理 🟢"}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "交互式首次向导，生成 config + gateway token 并启动 daemon",
		Args:  cobra.NoArgs,
		RunE:  run(configInit),
	}
	initCmd.Flags().Bool("non-interactive", false, "跳过向导（配合 --from-file）")
	initCmd.Flags().String("from-file", "", "从 JSON 文件导入配置（- 为 stdin）")
	initCmd.Flags().Bool("force", false, "已存在 config 时覆盖")
	initCmd.Flags().Bool("no-start", false, "只生成配置，不自动启动 daemon")

	showCmd := &cobra.Command{Use: "show", Short: "显示当前 profile 配置（敏感字段遮罩）", Args: cobra.NoArgs, RunE: run(configShow)}
	showCmd.Flags().Bool("show-secrets", false, "输出敏感字段明文（需 TTY）")

	setCmd := &cobra.Command{Use: "set <key> <value>", Short: "设置单个配置项（点号路径）", Args: cobra.ExactArgs(2), RunE: run(configSet)}
	unsetCmd := &cobra.Command{Use: "unset <key>", Short: "删除单个配置项", Args: cobra.ExactArgs(1), RunE: run(configUnset)}

	c.AddCommand(initCmd, showCmd, setCmd, unsetCmd)
	return c
}

type initOpts struct {
	force          bool
	nonInteractive bool
	fromFile       string
	noStart        bool
}

func configInit(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	return initCore(ctx, initOpts{
		force:          flagBool(cmd, "force"),
		nonInteractive: flagBool(cmd, "non-interactive"),
		fromFile:       flagStr(cmd, "from-file"),
		noStart:        flagBool(cmd, "no-start"),
	})
}

func initCore(ctx *clictx.Context, o initOpts) (any, error) {
	force := o.force
	if config.Exists(ctx.Paths) && !force {
		return nil, errs.New(errs.CodeAlreadyExists, "profile `"+ctx.Profile+"` 已初始化",
			map[string]any{"hint": "加 --force 覆盖，或换一个 --profile", "checkedPaths": []string{ctx.Paths.Config}})
	}
	if err := fsutil.EnsureDir(ctx.Paths.Dir, fsutil.DirMode); err != nil {
		return nil, err
	}
	cfg := config.Default(ctx.Paths.Credentials)

	nonInteractive := o.nonInteractive
	fromFile := o.fromFile
	if nonInteractive || !prompt.IsInteractive() {
		if fromFile == "" {
			return nil, errs.New(errs.CodeNotInteractive, "非交互模式必须提供 --from-file <config.json>（- 为 stdin）")
		}
		raw, err := readConfigImport(fromFile)
		if err != nil {
			return nil, err
		}
		// 覆盖默认结构体 = deepMerge 语义；随后 version 回到默认。
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "无法解析配置文件："+fromFile)
		}
		cfg.Version = config.ConfigVersion
	} else {
		existing := creds.ResolveAPIKey()
		if existing.Value != "" {
			os.Stderr.WriteString("已检测到 Relay api-key（来源 " + existing.Source + "），跳过设置\n")
		} else {
			key, err := prompt.Ask("Relay api-key（ock_… ，留空可稍后 yc auth set-api-key 设置）", "")
			if err != nil {
				return nil, err
			}
			if key != "" {
				if _, err := creds.SetAPIKey(key, false); err != nil {
					return nil, err
				}
			}
		}
		enableEval, err := prompt.Confirm("启用灯效 webhook 评估器？", false)
		if err != nil {
			return nil, err
		}
		if enableEval {
			ev := config.DefaultEvaluator(ctx.Paths.Credentials)
			url, err := prompt.Ask("评估器 webhook URL", "")
			if err != nil {
				return nil, err
			}
			ev.WebhookURL = url
			cfg.LightRules.Evaluator = &ev
		}
	}

	token := creds.GenerateToken(32)
	if err := config.Save(ctx.Paths, cfg); err != nil {
		return nil, err
	}
	if _, err := creds.WriteGatewayToken(cfg, token); err != nil {
		return nil, err
	}

	// init 即用：默认把 daemon 拉起来；--no-start 跳过。失败不阻断 init（配置已落盘）。
	apiKey := creds.ResolveAPIKey()
	daemonInfo := map[string]any{"started": false}
	if !o.noStart {
		daemonInfo = startDaemonForInit(ctx)
	}
	started, _ := daemonInfo["started"].(bool)
	hint := initHint(started, apiKey.Value != "")
	return map[string]any{
		"ok":      true,
		"profile": ctx.Profile,
		"daemon":  daemonInfo,
		"relay": map[string]any{
			"url":           cfg.Relay.URL,
			"enabled":       cfg.Relay.Enabled,
			"apiKeyPresent": apiKey.Value != "",
		},
		"gatewayToken": token,
		"configPath":   ctx.Paths.Config,
		"hint":         hint,
	}, nil
}

func readConfigImport(fromFile string) ([]byte, error) {
	if fromFile == "-" {
		s, err := prompt.ReadStdin()
		if err != nil {
			return nil, err
		}
		return []byte(s), nil
	}
	raw, err := os.ReadFile(fromFile)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidArgument, "无法读取配置文件："+fromFile)
	}
	return raw, nil
}

func configShow(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if !config.Exists(ctx.Paths) {
		return nil, errs.New(errs.CodeConfigInvalid, "profile `"+ctx.Profile+"` 尚未初始化",
			map[string]any{"hint": "先运行 yoooclaw config init", "checkedPaths": []string{ctx.Paths.Config}})
	}
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return nil, err
	}
	if flagBool(cmd, "show-secrets") {
		if !prompt.IsInteractive() {
			return nil, errs.New(errs.CodeNotInteractive, "--show-secrets 需要在 TTY 中运行")
		}
		ok, err := prompt.Confirm("确认明文输出敏感字段？", false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.New(errs.CodeConfirmationRequired, "已取消")
		}
		return config.ToMap(cfg), nil
	}
	return config.Mask(config.ToMap(cfg)), nil
}

func configSet(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	key, value := args[0], args[1]
	if key == "version" {
		return nil, errs.New(errs.CodeInvalidArgument, "version 字段不可修改")
	}
	m, err := config.LoadMergedMap(ctx.Paths)
	if err != nil {
		return nil, err
	}
	coerced, err := config.CoerceValue(key, value)
	if err != nil {
		return nil, err
	}
	config.SetByPath(m, key, coerced)
	if err := config.SaveRaw(ctx.Paths, m); err != nil {
		return nil, err
	}
	got, _ := config.GetByPath(m, key)
	return map[string]any{"ok": true, "key": key, "value": got}, nil
}

func configUnset(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	key := args[0]
	m, err := config.LoadMergedMap(ctx.Paths)
	if err != nil {
		return nil, err
	}
	removed := config.UnsetByPath(m, key)
	if removed {
		if err := config.SaveRaw(ctx.Paths, m); err != nil {
			return nil, err
		}
	}
	return map[string]any{"ok": true, "key": key, "removed": removed}, nil
}

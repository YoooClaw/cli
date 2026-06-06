package cli

import (
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	c := &cobra.Command{Use: "auth", Short: "凭据与鉴权 🟢/🟡"}

	setKey := &cobra.Command{Use: "set-api-key <key>", Short: "设置/轮换 account 级 default api-key（- 从 stdin 读）🟢", Args: cobra.ExactArgs(1), RunE: run(authSetAPIKey)}
	setKey.Flags().Bool("keychain", false, "写入 OS keychain 而非文件")

	addKey := &cobra.Command{Use: "add-api-key <key>", Short: "新增一条 multi-key api-key（- 从 stdin 读）🟢", Args: cobra.ExactArgs(1), RunE: run(authAddAPIKey)}
	addKey.Flags().String("label", "", "api-key label（[a-z0-9-]{1,32}）")
	addKey.Flags().Bool("default", false, "设为 default key")
	addKey.Flags().Bool("force", false, "label 已存在时覆盖")

	listKeys := &cobra.Command{Use: "list-api-keys", Short: "列出 api-key 条目（key 自动遮罩）🟢", Args: cobra.NoArgs, RunE: run(authListAPIKeys)}
	rmKey := &cobra.Command{Use: "remove-api-key <label>", Short: "删除指定 label 的 api-key 🟢", Args: cobra.ExactArgs(1), RunE: run(authRemoveAPIKey)}
	setDefault := &cobra.Command{Use: "set-default-api-key <label>", Short: "切换 default api-key 🟢", Args: cobra.ExactArgs(1), RunE: run(authSetDefaultAPIKey)}

	rotate := &cobra.Command{Use: "token-rotate", Short: "生成新 gateway token；daemon 在跑时随后 restart 生效 🟢", Args: cobra.NoArgs, RunE: run(authTokenRotate)}
	rotate.Flags().String("length", "32", "token 字节长度")

	status := &cobra.Command{Use: "status", Short: "显示鉴权状态（本地检查，不调 daemon）🟢", Args: cobra.NoArgs, RunE: run(authStatus)}
	check := &cobra.Command{Use: "check", Short: "端到端鉴权体检（调 daemon /daemon/status）🟡", Args: cobra.NoArgs, RunE: run(authCheck)}

	c.AddCommand(setKey, addKey, listKeys, rmKey, setDefault, rotate, status, check)
	return c
}

func readKeyArg(arg string) (string, error) {
	if arg == "-" {
		s, err := prompt.ReadStdin()
		if err != nil {
			return "", err
		}
		arg = strings.TrimSpace(s)
	}
	if arg == "" {
		return "", errs.New(errs.CodeInvalidArgument, "api-key 不能为空")
	}
	return arg, nil
}

func authSetAPIKey(_ *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	key, err := readKeyArg(args[0])
	if err != nil {
		return nil, err
	}
	result, err := creds.SetAPIKey(key, flagBool(cmd, "keychain"))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "source": result.Source, "location": result.Location, "label": result.Label,
		"masked": creds.MaskSecret(result.Value),
		"hint":   "插件 / CLI / daemon 共用同一份；daemon 在跑时会经文件 watch 热生效",
	}, nil
}

func authAddAPIKey(_ *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	key, err := readKeyArg(args[0])
	if err != nil {
		return nil, err
	}
	label := flagStr(cmd, "label")
	if label == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--label 必填")
	}
	before := creds.ResolveAPIKeyEntries()
	result, err := creds.AddAPIKey(key, label, flagBool(cmd, "default"), flagBool(cmd, "force"))
	if err != nil {
		return nil, err
	}
	after := creds.ResolveAPIKeyEntries()
	isDefault := after.DefaultEntry != nil && after.DefaultEntry.Label == label
	return map[string]any{
		"ok": true, "mode": after.Mode, "label": label, "default": isDefault,
		"source": result.Source, "location": result.Location, "masked": creds.MaskSecret(key),
		"shadowedKeychainPresent": after.ShadowedKeychainPresent,
		"migratedLegacyApiKey":    before.Mode == "legacy-file-single",
		"hint":                    "daemon 在跑时会经文件 watch 热生效；watch 不可靠时执行 yc daemon reload",
	}, nil
}

func entryToItem(e creds.ApiKeyEntry) map[string]any {
	return map[string]any{"label": e.Label, "default": e.Default, "source": e.Source, "masked": creds.MaskSecret(e.Key)}
}

func entriesToItems(entries []creds.ApiKeyEntry) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryToItem(e))
	}
	return out
}

func defaultLabelOrNil(set creds.CredentialSet) any {
	if set.DefaultEntry != nil {
		return set.DefaultEntry.Label
	}
	return nil
}

func authListAPIKeys(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	set := creds.ResolveAPIKeyEntries()
	return map[string]any{
		"ok": true, "mode": set.Mode, "defaultLabel": defaultLabelOrNil(set), "location": set.Location,
		"legacyApiKeyPresent": set.LegacyAPIKeyPresent, "shadowedKeychainPresent": set.ShadowedKeychainPresent,
		"warnings": set.Warnings, "items": entriesToItems(set.Entries),
	}, nil
}

func authRemoveAPIKey(_ *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	label := args[0]
	_, removed, newDefault, err := creds.RemoveAPIKey(label)
	if err != nil {
		return nil, err
	}
	set := creds.ResolveAPIKeyEntries()
	var nd any
	if newDefault != "" {
		nd = newDefault
	}
	return map[string]any{
		"ok": true, "removed": removed, "mode": set.Mode, "defaultLabel": defaultLabelOrNil(set),
		"newDefault": nd, "remaining": len(set.Entries),
	}, nil
}

func authSetDefaultAPIKey(_ *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	result, err := creds.SetDefaultAPIKey(args[0])
	if err != nil {
		return nil, err
	}
	set := creds.ResolveAPIKeyEntries()
	return map[string]any{
		"ok": true, "mode": set.Mode, "defaultLabel": defaultLabelOrNil(set),
		"source": result.Source, "location": result.Location,
	}, nil
}

func authStatus(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	apiKey := creds.ResolveAPIKey()
	apiKeys := creds.ResolveAPIKeyEntries()
	var tokenInfo map[string]any
	if config.Exists(ctx.Paths) {
		cfg, err := config.Load(ctx.Paths)
		if err == nil {
			token, _ := creds.ResolveGatewayToken(cfg)
			tokenInfo = map[string]any{"present": token.Value != "", "source": token.Source, "location": token.Location, "masked": creds.MaskSecret(token.Value)}
		}
	}
	if tokenInfo == nil {
		tokenInfo = map[string]any{"present": false}
	}
	state := daemon.State(ctx.Paths)
	daemonInfo := map[string]any{"running": state.Running, "stale": state.Stale}
	if state.Lock != nil {
		daemonInfo["pid"] = state.Lock.PID
	}
	return map[string]any{
		"ok": true, "profile": ctx.Profile, "mode": apiKeys.Mode, "defaultLabel": defaultLabelOrNil(apiKeys),
		"apiKeys": entriesToItems(apiKeys.Entries), "legacyApiKeyPresent": apiKeys.LegacyAPIKeyPresent,
		"shadowedKeychainPresent": apiKeys.ShadowedKeychainPresent, "warnings": apiKeys.Warnings,
		"apiKey": map[string]any{
			"present": apiKey.Value != "", "source": apiKey.Source, "location": apiKey.Location,
			"label": apiKey.Label, "masked": creds.MaskSecret(apiKey.Value),
		},
		"gatewayToken": tokenInfo,
		"daemon":       daemonInfo,
	}, nil
}

func authTokenRotate(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	if !config.Exists(ctx.Paths) {
		return nil, errs.New(errs.CodeConfigInvalid, "profile `"+ctx.Profile+"` 尚未初始化",
			map[string]any{"hint": "先运行 yoooclaw config init"})
	}
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return nil, err
	}
	numBytes := 32
	if s := flagStr(cmd, "length"); s != "" {
		n, convErr := strconv.Atoi(s)
		if convErr != nil || n < 16 {
			return nil, errs.New(errs.CodeInvalidArgument, "--length 至少 16 字节")
		}
		numBytes = n
	}
	token := creds.GenerateToken(numBytes)
	written, err := creds.WriteGatewayToken(cfg, token)
	if err != nil {
		return nil, err
	}
	state := daemon.State(ctx.Paths)
	hot := "daemon 未运行：下次启动即用新 token"
	if state.Running {
		hot = "daemon 在运行：请执行 yoooclaw daemon restart 使新 token 生效"
	}
	return map[string]any{"ok": true, "token": token, "source": written.Source, "location": written.Location, "hint": hot}, nil
}

func authCheck(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	if err := daemon.AssertRunning(ctx.Paths); err != nil {
		return nil, err
	}
	status, body, err := daemon.NewClient(ctx.Paths).Request("GET", "/daemon/status", nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": status >= 200 && status < 300, "profile": ctx.Profile,
		"daemonReachable": true, "status": status, "daemon": body,
	}, nil
}

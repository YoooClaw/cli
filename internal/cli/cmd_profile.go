package cli

import (
	"os"
	"sort"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/spf13/cobra"
)

func newProfileCmd() *cobra.Command {
	c := &cobra.Command{Use: "profile", Short: "多 profile 管理 🟢"}

	listCmd := &cobra.Command{Use: "list", Short: "列出所有 profile，标注 active", Args: cobra.NoArgs, RunE: run(profileList)}
	useCmd := &cobra.Command{Use: "use <name>", Short: "切换 active profile", Args: cobra.ExactArgs(1), RunE: run(profileUse)}
	createCmd := &cobra.Command{Use: "create <name>", Short: "新建 profile（走 config init 向导）", Args: cobra.ExactArgs(1), RunE: run(profileCreate)}
	createCmd.Flags().Bool("non-interactive", false, "跳过向导（配合 --from-file）")
	createCmd.Flags().String("from-file", "", "从 JSON 文件导入配置（- 为 stdin）")
	createCmd.Flags().Bool("force", false, "已存在 config 时覆盖")
	createCmd.Flags().Bool("no-start", false, "只生成配置，不自动启动 daemon")
	deleteCmd := &cobra.Command{Use: "delete <name>", Short: "删除 profile（非 active，需 --yes）", Args: cobra.ExactArgs(1), RunE: run(profileDelete)}
	deleteCmd.Flags().Bool("yes", false, "跳过确认")

	c.AddCommand(listCmd, useCmd, createCmd, deleteCmd)
	return c
}

func profileList(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	active := paths.ReadActiveProfile()
	if active == "" {
		active = paths.DefaultProfile
	}
	names := paths.ListProfileNames()
	for _, implied := range []string{active, paths.DefaultProfile} {
		if !contains(names, implied) {
			names = append(names, implied)
		}
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{
			"name":        name,
			"active":      name == active,
			"initialized": config.Exists(paths.For(name)),
		})
	}
	return out, nil
}

func profileUse(_ *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	name := args[0]
	p := paths.For(name)
	if !fsutil.Exists(p.Dir) {
		return nil, errs.New(errs.CodeProfileNotFound, "profile `"+name+"` 不存在",
			map[string]any{"hint": "用 yoooclaw profile create 新建", "checkedPaths": []string{p.Dir}})
	}
	if err := fsutil.WriteAtomic(paths.ActiveProfilePath(), []byte(name+"\n"), fsutil.ConfigFileMode); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "active": name}, nil
}

func profileCreate(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	name := args[0]
	p := paths.For(name)
	if config.Exists(p) {
		return nil, errs.New(errs.CodeAlreadyExists, "profile `"+name+"` 已存在")
	}
	subCtx := &clictx.Context{Profile: name, Paths: p, Format: ctx.Format, Quiet: ctx.Quiet, Color: ctx.Color}
	return initCore(subCtx, initOpts{
		force:          flagBool(cmd, "force"),
		nonInteractive: flagBool(cmd, "non-interactive"),
		fromFile:       flagStr(cmd, "from-file"),
		noStart:        flagBool(cmd, "no-start"),
	})
}

func profileDelete(_ *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	name := args[0]
	active := paths.ReadActiveProfile()
	if active == "" {
		active = paths.DefaultProfile
	}
	if name == active {
		return nil, errs.New(errs.CodeInvalidArgument, "不能删除当前 active profile `"+name+"`",
			map[string]any{"hint": "先 yoooclaw profile use <其他 profile> 再删除"})
	}
	p := paths.For(name)
	if !fsutil.Exists(p.Dir) {
		return nil, errs.New(errs.CodeProfileNotFound, "profile `"+name+"` 不存在")
	}
	if !flagBool(cmd, "yes") {
		ok, err := prompt.Confirm("确认删除 profile `"+name+"` 及其全部数据？", false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.New(errs.CodeConfirmationRequired, "已取消")
		}
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "deleted": name}, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

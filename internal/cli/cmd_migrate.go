package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/spf13/cobra"
)

// apiKeyField 是共享 credentials.json 里的 legacy api-key 字段名（与 creds 包一致）。
const apiKeyField = "apiKey"

func newMigrateCmd() *cobra.Command {
	c := &cobra.Command{Use: "migrate", Short: "从 openclaw 插件迁移数据 🟢"}
	fromOC := &cobra.Command{
		Use:   "from-openclaw",
		Short: "迁移 notifications/recordings/规则/api-key 到 ~/.yoooclaw",
		Args:  cobra.NoArgs,
		RunE:  run(migrateFromOpenclaw),
	}
	fromOC.Flags().Bool("dry-run", false, "只打印迁移计划，不写入")
	fromOC.Flags().String("source", "", "自定义源目录，默认 ~/.openclaw")
	c.AddCommand(fromOC)
	return c
}

type subdirPlan struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Exists    bool   `json:"exists"`
	FileCount int    `json:"fileCount"`
}

func migrateFromOpenclaw(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	sourceRoot := flagStr(cmd, "source")
	if sourceRoot == "" {
		home, _ := os.UserHomeDir()
		sourceRoot = filepath.Join(home, ".openclaw")
	}
	pluginRoot := filepath.Join(sourceRoot, "plugins", "phone-notifications")

	defs := []struct{ name, source, target string }{
		{"notifications", filepath.Join(pluginRoot, "notifications"), ctx.Paths.Notifications},
		{"recordings", filepath.Join(pluginRoot, "recordings"), ctx.Paths.Recordings},
		{"light-rules", filepath.Join(pluginRoot, "light-rules"), ctx.Paths.LightRules},
		{"images", filepath.Join(pluginRoot, "images"), ctx.Paths.Images},
	}
	plans := make([]subdirPlan, 0, len(defs))
	for _, d := range defs {
		plans = append(plans, subdirPlan{
			Name: d.name, Source: d.source, Target: d.target,
			Exists: fsutil.Exists(d.source), FileCount: fsutil.CountFiles(d.source),
		})
	}

	// api-key 迁移计划。
	var srcCreds map[string]any
	_, _ = fsutil.ReadJSON(filepath.Join(sourceRoot, "credentials.json"), &srcCreds)
	srcAPIKey, _ := srcCreds[apiKeyField].(string)
	sharedPath := paths.SharedCredentialsPath()
	var existingShared map[string]any
	_, _ = fsutil.ReadJSON(sharedPath, &existingShared)
	apiKeyAction := "none"
	if srcAPIKey != "" {
		if existingShared[apiKeyField] != nil {
			apiKeyAction = "skip-existing"
		} else {
			apiKeyAction = "copy"
		}
	}

	backupDir := ctx.Paths.Dir + ".bak-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	willBackup := fsutil.Exists(ctx.Paths.Dir) && dirNonEmpty(ctx.Paths.Dir)

	plansAny := make([]any, 0, len(plans))
	for _, p := range plans {
		plansAny = append(plansAny, map[string]any{
			"name": p.Name, "source": p.Source, "target": p.Target, "exists": p.Exists, "fileCount": p.FileCount,
		})
	}

	if flagBool(cmd, "dry-run") {
		return map[string]any{
			"ok": true, "dryRun": true, "source": sourceRoot, "profile": ctx.Profile,
			"backup": backupOrNil(willBackup, backupDir), "subdirs": plansAny,
			"apiKey": map[string]any{"action": apiKeyAction},
			"hint":   "迁移前请先停止 openclaw 客户端，避免插件还在写 notifications 导致数据竞争",
		}, nil
	}

	if willBackup {
		if err := fsutil.CopyDir(ctx.Paths.Dir, backupDir); err != nil {
			return nil, err
		}
	}
	if err := fsutil.EnsureDir(ctx.Paths.Dir, fsutil.DirMode); err != nil {
		return nil, err
	}

	migrated := []string{}
	for _, p := range plans {
		if !p.Exists {
			continue
		}
		if err := fsutil.CopyDir(p.Source, p.Target); err != nil {
			return nil, err
		}
		migrated = append(migrated, p.Name)
	}

	apiKeyResult := apiKeyAction
	if apiKeyAction == "copy" && srcAPIKey != "" {
		data := existingShared
		if data == nil {
			data = map[string]any{}
		}
		data[apiKeyField] = srcAPIKey
		if err := fsutil.WriteJSON(sharedPath, data, fsutil.SecretFileMode); err != nil {
			return nil, err
		}
		apiKeyResult = "copied"
	}

	return map[string]any{
		"ok": true, "source": sourceRoot, "profile": ctx.Profile,
		"backup": backupOrNil(willBackup, backupDir), "migrated": migrated,
		"apiKey": map[string]any{"action": apiKeyResult},
	}, nil
}

func dirNonEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func backupOrNil(willBackup bool, dir string) any {
	if willBackup {
		return dir
	}
	return nil
}

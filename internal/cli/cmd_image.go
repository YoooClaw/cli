package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/image"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/spf13/cobra"
)

func newImageCmd() *cobra.Command {
	c := &cobra.Command{Use: "image", Short: "图片管理 🟢"}

	list := &cobra.Command{Use: "list", Short: "列出所有图片", Args: cobra.NoArgs, RunE: run(imageList)}
	list.Flags().String("status", "", "syncing|synced|sync_failed")
	list.Flags().String("app", "", "按来源应用过滤")
	list.Flags().String("client", "", "按 clientLabel 过滤；all 为全部")
	list.Flags().String("from", "", "created_at 起")
	list.Flags().String("to", "", "created_at 止")
	list.Flags().String("limit", "100", "最大返回条数")

	status := &cobra.Command{Use: "status <id>", Short: "查看单张图片详情", Args: cobra.ExactArgs(1), RunE: run(imageStatus)}
	pathCmd := &cobra.Command{Use: "path <id>", Short: "打印图片本地文件绝对路径", Args: cobra.ExactArgs(1), RunE: run(imagePath)}
	pathCmd.Flags().Bool("thumbnail", false, "返回缩略图路径（若有）")
	storagePath := &cobra.Command{Use: "storage-path", Short: "打印图片存储目录绝对路径", Args: cobra.NoArgs, RunE: run(imageStoragePath)}
	latest := &cobra.Command{Use: "+latest", Short: "展示最新一张图片详情", Args: cobra.NoArgs, RunE: run(imageLatest)}

	c.AddCommand(list, status, pathCmd, storagePath, latest)
	return c
}

func imageEntryAsAny(e image.Entry) any { return e }

func imageList(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	items := image.ReadIndex(ctx.Paths.Images)
	status := flagStr(cmd, "status")
	client := flagStr(cmd, "client")
	app := flagStr(cmd, "app")
	from := flagStr(cmd, "from")
	to := flagStr(cmd, "to")

	out := make([]image.Entry, 0, len(items))
	for _, it := range items {
		if status != "" && it.Status != status {
			continue
		}
		if client != "" && client != "all" && labelOrLegacy2(it.ClientLabel) != client {
			continue
		}
		if app != "" && !notif.MatchesAppFilter(notif.StoredNotification{AppName: it.Metadata.SourceApp}, app) {
			continue
		}
		if from != "" && it.Metadata.CreatedAt < from {
			continue
		}
		if to != "" && it.Metadata.CreatedAt > to {
			continue
		}
		out = append(out, it)
	}
	image.SortByCreatedDesc(out)
	limit := atoiDefault(flagStr(cmd, "limit"), 100)
	if len(out) > limit {
		out = out[:limit]
	}
	arr := make([]any, 0, len(out))
	for _, e := range out {
		arr = append(arr, imageEntryAsAny(e))
	}
	return map[string]any{"ok": true, "total": len(out), "images": arr}, nil
}

func findImage(ctx *clictx.Context, id string) (image.Entry, bool) {
	for _, it := range image.ReadIndex(ctx.Paths.Images) {
		if it.ImageID == id {
			return it, true
		}
	}
	return image.Entry{}, false
}

func imageStatus(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	e, ok := findImage(ctx, args[0])
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "图片不存在："+args[0])
	}
	return map[string]any{"ok": true, "image": e}, nil
}

func imagePath(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	e, ok := findImage(ctx, args[0])
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "图片不存在："+args[0])
	}
	var rel *string
	if flagBool(cmd, "thumbnail") {
		rel = e.Thumbnail
	} else {
		rel = e.LocalFile
	}
	if e.Status != "synced" || rel == nil || *rel == "" {
		var lastErr any
		if e.LastError != nil {
			lastErr = *e.LastError
		}
		return nil, errs.New(errs.CodeImageNotReady, "图片 "+args[0]+" 尚未下载完成",
			map[string]any{"status": e.Status, "lastError": lastErr})
	}
	return map[string]any{"ok": true, "path": image.ResolveFile(ctx.Paths.Images, *rel)}, nil
}

func imageStoragePath(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return map[string]any{"ok": true, "path": ctx.Paths.Images}, nil
}

func imageLatest(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	items := image.ReadIndex(ctx.Paths.Images)
	image.SortByCreatedDesc(items)
	if len(items) == 0 {
		return map[string]any{"ok": true, "image": nil}, nil
	}
	return map[string]any{"ok": true, "image": items[0]}, nil
}

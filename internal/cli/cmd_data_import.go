package cli

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/spf13/cobra"
)

func dataImportRPC(ctx *clictx.Context, method, path string, body any) (any, error) {
	if e := daemon.AssertRunning(ctx.Paths); e != nil {
		return nil, e
	}
	status, result, e := daemon.NewClient(ctx.Paths).Request(method, path, body)
	if e != nil {
		return nil, e
	}
	return daemon.CheckedResponse(status, result)
}
func addDataImportCommands(root *cobra.Command) {
	c := &cobra.Command{Use: "import", Short: "从已上传的 OSS 对象异步导入（需要大小和 SHA-256）", Args: cobra.NoArgs, RunE: run(func(ctx *clictx.Context, c *cobra.Command, _ []string) (any, error) {
		size, _ := c.Flags().GetInt64("size")
		tags, _ := c.Flags().GetStringArray("tag")
		meta := flagStr(c, "meta")
		if strings.HasPrefix(meta, "@") {
			f, e := os.Open(strings.TrimPrefix(meta, "@"))
			if e != nil {
				return nil, e
			}
			b, e := io.ReadAll(io.LimitReader(f, (16<<10)+1))
			f.Close()
			if e != nil {
				return nil, e
			}
			meta = string(b)
		}
		if meta == "" {
			meta = "{}"
		}
		if len(meta) > 16<<10 || !json.Valid([]byte(meta)) {
			return nil, data.Error("INVALID_META", "invalid JSON metadata")
		}
		r := data.ImportRequest{Metadata: data.Metadata{Name: flagStr(c, "name"), Title: flagStr(c, "title"), MIME: flagStr(c, "mime"), Tags: tags, Note: flagStr(c, "note"), Source: flagStr(c, "source"), Meta: json.RawMessage(meta), IdempotencyKey: flagStr(c, "idempotency-key")}, URL: flagStr(c, "url"), OSSTaskID: flagStr(c, "oss-task-id"), SizeBytes: size, SHA256: flagStr(c, "sha256")}
		return dataImportRPC(ctx, "POST", "/data/import", r)
	})}
	for _, k := range []string{"url", "oss-task-id", "sha256", "name", "title", "mime", "note", "meta", "idempotency-key"} {
		c.Flags().String(k, "", k)
	}
	c.Flags().String("source", "cli", "来源")
	c.Flags().StringArray("tag", nil, "标签，可重复")
	c.Flags().Int64("size", 0, "原始字节数（必填）")
	root.AddCommand(c)
	status := &cobra.Command{Use: "import-status <task-id>", Short: "查询持久化导入状态", Args: cobra.ExactArgs(1), RunE: run(func(ctx *clictx.Context, c *cobra.Command, a []string) (any, error) {
		return dataImportRPC(ctx, "GET", "/data/imports/"+url.PathEscape(a[0]), nil)
	})}
	root.AddCommand(status)
	retry := &cobra.Command{Use: "import-retry <task-id>", Short: "重试失败的导入，可替换过期签名 URL", Args: cobra.ExactArgs(1), RunE: run(func(ctx *clictx.Context, c *cobra.Command, a []string) (any, error) {
		return dataImportRPC(ctx, "POST", "/data/imports/"+url.PathEscape(a[0])+"/retry", map[string]string{"url": flagStr(c, "url")})
	})}
	retry.Flags().String("url", "", "新的 OSS 签名 URL（可选）")
	root.AddCommand(retry)
}

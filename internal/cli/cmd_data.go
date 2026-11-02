package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/output"
	"github.com/spf13/cobra"
)

func newDataCmd() *cobra.Command {
	root := &cobra.Command{Use: "data", Short: "保存和检索任意文件（原始字节，不解析）", Long: "保存和检索任意文件。写入、删除和 GC 需要 daemon；读取可离线。文件未静态加密，请勿写入高敏感密钥。"}
	put := &cobra.Command{Use: "put [file|-]", Args: cobra.MaximumNArgs(1), Short: "保存文件、stdin 或 --text 文本；未加密，请勿保存高敏感密钥", RunE: run(dataPut)}
	for _, v := range []struct{ k, d string }{{"name", "展示文件名"}, {"title", "标题"}, {"mime", "声明 MIME"}, {"note", "备注"}, {"meta", "JSON 对象或 @file"}, {"idempotency-key", "重试幂等键"}, {"text", "直接写入文本"}, {"captured-at", "采集时间 RFC3339"}} {
		put.Flags().String(v.k, "", v.d)
	}
	put.Flags().String("source", "cli", "来源")
	put.Flags().StringArray("tag", nil, "标签，可重复")
	put.Flags().Int64("max-bytes", 0, "客户端上限，默认服务端配置")
	root.AddCommand(put)
	addDataImportCommands(root)
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "按元数据检索", RunE: run(func(ctx *clictx.Context, c *cobra.Command, _ []string) (any, error) {
		s, e := localData(ctx)
		if e != nil {
			return nil, e
		}
		tags, _ := c.Flags().GetStringArray("tag")
		limit, _ := c.Flags().GetInt("limit")
		return s.List(data.Filter{Type: flagStr(c, "type"), Tags: tags, Source: flagStr(c, "source"), Client: flagStr(c, "client"), From: flagStr(c, "from"), To: flagStr(c, "to"), Query: flagStr(c, "query"), Cursor: flagStr(c, "cursor"), Limit: limit})
	})}
	for _, k := range []string{"type", "source", "client", "from", "to", "query", "cursor"} {
		list.Flags().String(k, "", k)
	}
	list.Flags().StringArray("tag", nil, "标签 AND")
	list.Flags().Int("limit", 100, "每页 1..500")
	root.AddCommand(list)
	for _, mode := range []string{"show", "get", "path", "cat", "rm", "usage", "gc"} {
		use := mode
		args := cobra.NoArgs
		if mode != "usage" && mode != "gc" {
			use += " <id>"
			args = cobra.ExactArgs(1)
		}
		c := &cobra.Command{Use: use, Args: args, Short: "data " + mode}
		h := func(ctx *clictx.Context, c *cobra.Command, args []string) (any, error) {
			s, e := localData(ctx)
			if e != nil {
				return nil, e
			}
			if mode == "usage" {
				u, e := s.Usage()
				return map[string]any{"ok": true, "data": u}, e
			}
			if mode == "rm" || mode == "gc" {
				if mode == "rm" && !flagBool(c, "yes") {
					return nil, errs.New(errs.CodeConfirmationRequired, "删除需要 --yes")
				}
				if e = daemon.AssertRunning(ctx.Paths); e != nil {
					return nil, e
				}
				method, path := "POST", "/data/gc?dryRun="+fmt.Sprint(flagBool(c, "dry-run"))
				if mode == "rm" {
					method = "DELETE"
					path = "/data/" + url.PathEscape(args[0])
				}
				status, v, e := daemon.NewClient(ctx.Paths).Request(method, path, nil)
				if e != nil {
					return nil, e
				}
				return daemon.CheckedResponse(status, v)
			}
			o, e := s.Show(args[0], "")
			if e != nil {
				return nil, e
			}
			if mode == "show" {
				return map[string]any{"ok": true, "data": o}, nil
			}
			p, e := s.Path(o)
			if e != nil {
				return nil, e
			}
			if mode == "path" {
				_, e = fmt.Fprintln(c.OutOrStdout(), p)
				return nil, e
			}
			if mode == "cat" {
				if dataOutputTTY(c.OutOrStdout()) && o.Preview == nil && !flagBool(c, "force") {
					return nil, data.Error("INVALID_PARAMS", "binary output to TTY requires --force")
				}
				f, e := os.Open(p)
				if e != nil {
					return nil, e
				}
				defer f.Close()
				_, e = io.Copy(c.OutOrStdout(), f)
				return nil, e
			}
			out := flagStr(c, "out")
			if out == "" {
				return nil, data.Error("INVALID_PARAMS", "--out is required")
			}
			f, e := os.Open(p)
			if e != nil {
				return nil, e
			}
			defer f.Close()
			dest, e := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return nil, e
			}
			_, e = io.Copy(dest, f)
			closeErr := dest.Close()
			if e != nil {
				os.Remove(out)
				return nil, e
			}
			if closeErr != nil {
				os.Remove(out)
				return nil, closeErr
			}
			return map[string]any{"ok": true, "data": o}, nil
		}
		if mode == "cat" || mode == "path" {
			c.RunE = func(c *cobra.Command, a []string) error {
				ctx, e := buildContext(c)
				if e == nil {
					_, e = h(ctx, c, a)
				}
				if e != nil {
					output.RenderError(c.ErrOrStderr(), e, output.JSON)
					exitCode = 1
				}
				return nil
			}
		} else {
			c.RunE = run(h)
		}
		switch mode {
		case "get":
			c.Flags().String("out", "", "输出路径（必须不存在）")
		case "cat":
			c.Flags().Bool("force", false, "允许向终端输出二进制")
		case "rm":
			c.Flags().Bool("yes", false, "确认删除对象")
		case "gc":
			c.Flags().Bool("dry-run", false, "只查看待清理文件")
		}
		root.AddCommand(c)
	}
	return root
}
func localData(ctx *clictx.Context) (*data.Store, error) {
	cfg, e := config.Load(ctx.Paths)
	if e != nil {
		return nil, e
	}
	return data.New(ctx.Paths.Data, cfg.Data), nil
}
func dataOutputTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	i, e := f.Stat()
	return e == nil && i.Mode()&os.ModeCharDevice != 0
}
func dataPut(ctx *clictx.Context, c *cobra.Command, args []string) (any, error) {
	text := c.Flags().Changed("text")
	if text && len(args) > 0 || !text && len(args) != 1 {
		return nil, data.Error("INVALID_PARAMS", "choose file, stdin (-), or --text")
	}
	if e := daemon.AssertRunning(ctx.Paths); e != nil {
		return nil, e
	}
	s, e := localData(ctx)
	if e != nil {
		return nil, e
	}
	max, _ := c.Flags().GetInt64("max-bytes")
	if max < 0 {
		return nil, data.Error("INVALID_PARAMS", "max-bytes must be positive")
	}
	if max == 0 || max > s.Config.MaxObjectBytes {
		max = s.Config.MaxObjectBytes
	}
	var reader io.Reader
	name := flagStr(c, "name")
	if text {
		reader = strings.NewReader(flagStr(c, "text"))
	} else if args[0] == "-" {
		reader = c.InOrStdin()
	} else {
		f, e := os.Open(args[0])
		if e != nil {
			return nil, e
		}
		defer f.Close()
		i, e := f.Stat()
		if e != nil {
			return nil, e
		}
		if i.Size() > max {
			return nil, data.Error("TOO_LARGE", "file exceeds client limit")
		}
		reader = f
		if name == "" {
			name = filepath.Base(args[0])
		}
	}
	// Spool stdin with a bounded copy so oversized input is rejected before upload.
	tmp, e := os.CreateTemp("", "yc-data-")
	if e != nil {
		return nil, e
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, e := io.Copy(tmp, io.LimitReader(reader, max+1))
	if e != nil {
		return nil, e
	}
	if n > max {
		return nil, data.Error("TOO_LARGE", "input exceeds client limit")
	}
	if _, e = tmp.Seek(0, 0); e != nil {
		return nil, e
	}
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
	if meta != "" {
		var obj map[string]any
		if len(meta) > 16<<10 || json.Unmarshal([]byte(meta), &obj) != nil || obj == nil {
			return nil, data.Error("INVALID_META", "meta must be a JSON object <= 16 KiB")
		}
	}
	q := url.Values{"name": {name}, "title": {flagStr(c, "title")}, "source": {flagStr(c, "source")}, "note": {flagStr(c, "note")}, "meta": {meta}, "capturedAt": {flagStr(c, "captured-at")}}
	q["tag"], _ = c.Flags().GetStringArray("tag")
	media := flagStr(c, "mime")
	if media == "" {
		media = mime.TypeByExtension(filepath.Ext(name))
	}
	if media == "" {
		media = "application/octet-stream"
	}
	return daemon.NewClient(ctx.Paths).WithTimeout(2*time.Minute).RequestRaw("/data?"+q.Encode(), tmp, media, flagStr(c, "idempotency-key"))
}

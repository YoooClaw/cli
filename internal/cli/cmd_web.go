package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/webpage"
	"github.com/spf13/cobra"
)

const (
	defaultWebSearchLimit = 20
	maxWebSnippetsPerPage = 3
	maxWebSnippetRunes    = 240
)

func newSyncedWebPageCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "synced-web-page",
		Short: "本地已同步网页管理，不访问互联网 🟢",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "按收藏时间倒序列出所有已同步网页 🟢",
		Args:  cobra.NoArgs,
		RunE:  run(webList),
	}
	pathCmd := &cobra.Command{
		Use:   "path <urlHash>",
		Short: "根据 URL 哈希打印网页 Markdown 文件绝对路径 🟢",
		Args:  cobra.ExactArgs(1),
		RunE:  run(webPath),
	}
	search := &cobra.Command{
		Use:   "search <keyword>",
		Short: "在已同步网页的标题、URL 和 Markdown 正文中搜索 🟢",
		Args:  cobra.ExactArgs(1),
		RunE:  run(webSearch),
	}
	search.Flags().String("limit", strconv.Itoa(defaultWebSearchLimit), "最多返回的网页数量（默认 20）")
	storagePath := &cobra.Command{
		Use:   "storage-path",
		Short: "查询网页存储路径 🟢",
		Args:  cobra.NoArgs,
		RunE:  run(webStoragePath),
	}

	c.AddCommand(list, pathCmd, search, storagePath)
	return c
}

func webList(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	entries := webpage.ReadIndex(ctx.Paths.WebPages)
	webpage.SortByCapturedDesc(entries)

	pages := make([]any, 0, len(entries))
	for _, entry := range entries {
		pages = append(pages, map[string]any{
			"urlHash":         entry.URLHash,
			"title":           entry.Title,
			"siteName":        nilIfEmpty(entry.SiteName),
			"canonicalUrl":    entry.CanonicalURL,
			"capturedAt":      entry.CapturedAt,
			"firstCapturedAt": entry.FirstCapturedAt,
			"captureCount":    entry.CaptureCount,
			"bytes":           entry.Bytes,
			"relativePath":    entry.RelativePath,
			"hasArchive":      entry.ArchivePath != "",
			"clientLabel":     nilIfEmpty(entry.ClientLabel),
		})
	}
	return map[string]any{"ok": true, "total": len(pages), "pages": pages}, nil
}

func findWebPageByHash(entries []webpage.Entry, rawHash string) (webpage.Entry, bool) {
	hash := strings.ToLower(strings.TrimSpace(rawHash))
	for _, entry := range entries {
		if strings.ToLower(entry.URLHash) == hash {
			return entry, true
		}
	}
	if len(hash) < 8 {
		return webpage.Entry{}, false
	}

	var match webpage.Entry
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.URLHash), hash) {
			match = entry
			count++
		}
	}
	return match, count == 1
}

func resolveWebPageFile(dir, relativePath string) (string, bool) {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return "", false
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.Abs(filepath.Join(root, relativePath))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return resolved, true
}

func webPath(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	entry, ok := findWebPageByHash(webpage.ReadIndex(ctx.Paths.WebPages), args[0])
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "网页不存在："+args[0])
	}
	path, ok := resolveWebPageFile(ctx.Paths.WebPages, entry.RelativePath)
	if !ok {
		return nil, errs.New(
			errs.CodeStorageUnavailable,
			"网页 Markdown 文件路径不可用："+args[0],
		)
	}
	return map[string]any{"ok": true, "path": path}, nil
}

type webSearchMatch struct {
	URLHash       string   `json:"urlHash"`
	Title         string   `json:"title"`
	SiteName      any      `json:"siteName"`
	CanonicalURL  string   `json:"canonicalUrl"`
	CapturedAt    string   `json:"capturedAt"`
	Path          string   `json:"path"`
	MatchedFields []string `json:"matchedFields"`
	Matches       []string `json:"matches"`
}

func matchingWebMetadataFields(entry webpage.Entry, keyword string) []string {
	fields := []struct {
		name  string
		value string
	}{
		{"title", entry.Title},
		{"siteName", entry.SiteName},
		{"canonicalUrl", entry.CanonicalURL},
		{"url", entry.URL},
	}
	matches := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field.value), keyword) {
			matches = append(matches, field.name)
		}
	}
	return matches
}

func markdownBody(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return content
}

func clipWebSnippet(line, keyword string) string {
	compact := strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
	runes := []rune(compact)
	if len(runes) <= maxWebSnippetRunes {
		return compact
	}

	byteIndex := strings.Index(strings.ToLower(compact), keyword)
	runeIndex := 0
	if byteIndex > 0 {
		runeIndex = utf8.RuneCountInString(compact[:byteIndex])
	}
	start := runeIndex - maxWebSnippetRunes/3
	if start < 0 {
		start = 0
	}
	end := start + maxWebSnippetRunes
	if end > len(runes) {
		end = len(runes)
	}
	var result strings.Builder
	if start > 0 {
		result.WriteString("…")
	}
	result.WriteString(string(runes[start:end]))
	if end < len(runes) {
		result.WriteString("…")
	}
	return result.String()
}

func searchWebPages(dir string, entries []webpage.Entry, rawKeyword string, limit int) []webSearchMatch {
	keyword := strings.ToLower(strings.TrimSpace(rawKeyword))
	results := make([]webSearchMatch, 0)
	if keyword == "" {
		return results
	}

	webpage.SortByCapturedDesc(entries)
	for _, entry := range entries {
		if len(results) >= limit {
			break
		}
		path, ok := resolveWebPageFile(dir, entry.RelativePath)
		if !ok {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		matchedFields := matchingWebMetadataFields(entry, keyword)
		matches := make([]string, 0, maxWebSnippetsPerPage)
		seen := map[string]bool{}
		for _, line := range strings.Split(markdownBody(string(raw)), "\n") {
			if !strings.Contains(strings.ToLower(line), keyword) {
				continue
			}
			snippet := clipWebSnippet(line, keyword)
			if snippet == "" || seen[snippet] {
				continue
			}
			seen[snippet] = true
			matches = append(matches, snippet)
			if len(matches) >= maxWebSnippetsPerPage {
				break
			}
		}
		if len(matchedFields) == 0 && len(matches) == 0 {
			continue
		}
		results = append(results, webSearchMatch{
			URLHash:       entry.URLHash,
			Title:         entry.Title,
			SiteName:      nilIfEmpty(entry.SiteName),
			CanonicalURL:  entry.CanonicalURL,
			CapturedAt:    entry.CapturedAt,
			Path:          path,
			MatchedFields: matchedFields,
			Matches:       matches,
		})
	}
	return results
}

func positiveIntFlag(cmd *cobra.Command, name string, fallback int) (int, error) {
	raw := flagStr(cmd, name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument, "--%s 必须是正整数", name)
	}
	return value, nil
}

func webSearch(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	keyword := args[0]
	if strings.TrimSpace(keyword) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "搜索关键词不能为空")
	}
	limit, err := positiveIntFlag(cmd, "limit", defaultWebSearchLimit)
	if err != nil {
		return nil, err
	}
	pages := searchWebPages(
		ctx.Paths.WebPages,
		webpage.ReadIndex(ctx.Paths.WebPages),
		keyword,
		limit,
	)
	return map[string]any{
		"ok": true, "keyword": keyword, "total": len(pages), "pages": pages,
	}, nil
}

func webStoragePath(ctx *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return map[string]any{"ok": true, "path": ctx.Paths.WebPages}, nil
}

package cli

import (
	"os"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/webpage"
	"github.com/spf13/cobra"
)

// 同一网页的历史版本（web-page-versions-prd §4）：versions 列出全部版本，diff 给出两版差异，
// path --version 取某一版的文件。数据只来自本机，不访问互联网。

const (
	defaultWebDiffContext  = 2
	defaultWebDiffMaxLines = 300
)

func newSyncedWebPageVersionCmds() []*cobra.Command {
	versions := &cobra.Command{
		Use:   "versions <urlHash>",
		Short: "按收藏时间列出同一网页的全部历史版本 🟢",
		Args:  cobra.ExactArgs(1),
		RunE:  run(webVersions),
	}
	diff := &cobra.Command{
		Use:   "diff <urlHash>",
		Short: "对比同一网页的两个版本（默认最新版对前一版）🟢",
		Args:  cobra.ExactArgs(1),
		RunE:  run(webDiff),
	}
	diff.Flags().String("from", "", "旧版本号（默认 --to 的前一版）")
	diff.Flags().String("to", "", "新版本号（默认最新版）")
	diff.Flags().String("context", strconv.Itoa(defaultWebDiffContext), "每处改动前后保留的上下文行数")
	diff.Flags().String("max-lines", strconv.Itoa(defaultWebDiffMaxLines), "patch 最多输出的行数，超出标 truncated")
	return []*cobra.Command{versions, diff}
}

// webPageHistory 找到网页并解析它的版本；从没建过链的网页返回单版本视图。
func webPageHistory(ctx *clictx.Context, rawHash string) (webpage.Entry, []webpage.VersionFile, *webpage.Chain, error) {
	entry, ok := findWebPageByHash(webpage.ReadIndex(ctx.Paths.WebPages), rawHash)
	if !ok {
		return entry, nil, nil, errs.New(errs.CodeNotFound, "网页不存在："+rawHash)
	}
	chain, ok := webpage.LoadChain(ctx.Paths.WebPages, entry)
	if !ok {
		return entry, []webpage.VersionFile{{
			ChainVersion: webpage.ChainVersion{
				Version:          1,
				CapturedAt:       entry.CapturedAt,
				FirstSeenAt:      entry.FirstCapturedAt,
				LastSeenAt:       entry.CapturedAt,
				ObservationCount: entry.CaptureCount,
				ContentHash:      entry.ContentHash,
				Bytes:            entry.Bytes,
			},
			RelativePath: entry.RelativePath,
			Current:      true,
		}}, nil, nil
	}
	return entry, webpage.VersionFiles(chain), &chain, nil
}

func webVersions(ctx *clictx.Context, _ *cobra.Command, args []string) (any, error) {
	entry, files, chain, err := webPageHistory(ctx, args[0])
	if err != nil {
		return nil, err
	}
	versions := make([]map[string]any, 0, len(files))
	for _, file := range files {
		path, ok := resolveWebPageFile(ctx.Paths.WebPages, file.RelativePath)
		if !ok {
			continue
		}
		item := map[string]any{
			"version":          file.Version,
			"path":             path,
			"capturedAt":       file.CapturedAt,
			"firstSeenAt":      file.FirstSeenAt,
			"lastSeenAt":       file.LastSeenAt,
			"observationCount": file.ObservationCount,
			"bytes":            file.Bytes,
			"changedFromPrev":  file.ChangedFromPrev,
			"current":          file.Current,
		}
		if file.Late {
			item["late"] = true
		}
		if file.Note != "" {
			item["note"] = file.Note
		}
		versions = append(versions, item)
	}

	result := map[string]any{
		"ok":           true,
		"urlHash":      entry.URLHash,
		"title":        entry.Title,
		"canonicalUrl": entry.CanonicalURL,
		// history=false：只收藏过一次（或历史从未开启），只有这一份正文，没有变化可言。
		"history":          chain != nil,
		"versionCount":     len(versions),
		"observationCount": entry.CaptureCount,
		"versions":         versions,
	}
	if chain != nil {
		result["tracking"] = chain.Tracking
		result["latestVersion"] = chain.LatestVersion
		result["firstCapturedAt"] = chain.FirstCapturedAt
		result["lastChangedAt"] = chain.LastChangedAt
		result["observationCount"] = chain.ObservationCount
	} else {
		result["tracking"] = nilIfEmpty(entry.Tracking)
	}
	return result, nil
}

func versionFlag(cmd *cobra.Command, name string) (int, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(flagStr(cmd, name)), "v")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument, "--%s 必须是正整数版本号", name)
	}
	return n, nil
}

func nonNegativeIntFlag(cmd *cobra.Command, name string, fallback int) (int, error) {
	raw := flagStr(cmd, name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument, "--%s 必须是非负整数", name)
	}
	return n, nil
}

func findVersion(files []webpage.VersionFile, version int) (webpage.VersionFile, bool) {
	for _, file := range files {
		if file.Version == version {
			return file, true
		}
	}
	return webpage.VersionFile{}, false
}

func readVersionBody(ctx *clictx.Context, file webpage.VersionFile) (string, string, error) {
	path, ok := resolveWebPageFile(ctx.Paths.WebPages, file.RelativePath)
	if !ok {
		return "", "", errs.New(errs.CodeStorageUnavailable, "版本文件路径不可用：v"+strconv.Itoa(file.Version))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", errs.New(errs.CodeStorageUnavailable, "读取版本文件失败：v"+strconv.Itoa(file.Version))
	}
	return webpage.BodyOf(string(raw)), path, nil
}

func webDiff(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	entry, files, chain, err := webPageHistory(ctx, args[0])
	if err != nil {
		return nil, err
	}
	if chain == nil || len(files) < 2 {
		return nil, errs.New(errs.CodeInvalidArgument, "这个网页只有一个版本，没有可对比的历史："+args[0])
	}
	toVersion, err := versionFlag(cmd, "to")
	if err != nil {
		return nil, err
	}
	fromVersion, err := versionFlag(cmd, "from")
	if err != nil {
		return nil, err
	}
	context, err := nonNegativeIntFlag(cmd, "context", defaultWebDiffContext)
	if err != nil {
		return nil, err
	}
	maxLines, err := positiveIntFlag(cmd, "max-lines", defaultWebDiffMaxLines)
	if err != nil {
		return nil, err
	}

	if toVersion == 0 {
		toVersion = chain.LatestVersion
	}
	to, ok := findVersion(files, toVersion)
	if !ok {
		return nil, errs.Newf(errs.CodeNotFound, "版本不存在：v%d", toVersion)
	}
	if fromVersion == 0 {
		// 默认对比「文件是怎么变成这一版的」：按版本号取前一版，而不是按时间（§4.4）。
		if to.Prev == nil {
			return nil, errs.Newf(errs.CodeInvalidArgument, "v%d 是第一个版本，请用 --from 指定对比对象", toVersion)
		}
		fromVersion = *to.Prev
	}
	from, ok := findVersion(files, fromVersion)
	if !ok {
		return nil, errs.Newf(errs.CodeNotFound, "版本不存在：v%d", fromVersion)
	}
	if from.Version == to.Version {
		return nil, errs.New(errs.CodeInvalidArgument, "--from 与 --to 是同一个版本")
	}

	fromBody, fromPath, err := readVersionBody(ctx, from)
	if err != nil {
		return nil, err
	}
	toBody, toPath, err := readVersionBody(ctx, to)
	if err != nil {
		return nil, err
	}
	diff := webpage.DiffBodies(fromBody, toBody, context)
	patch, truncated := renderPatch(from, to, diff, maxLines)

	side := func(file webpage.VersionFile, path string) map[string]any {
		out := map[string]any{"version": file.Version, "capturedAt": file.CapturedAt, "path": path}
		if file.Late {
			out["late"] = true
		}
		return out
	}
	return map[string]any{
		"ok":           true,
		"urlHash":      entry.URLHash,
		"title":        entry.Title,
		"canonicalUrl": entry.CanonicalURL,
		"from":         side(from, fromPath),
		"to":           side(to, toPath),
		"added":        diff.Added,
		"removed":      diff.Removed,
		"unchanged":    diff.Added == 0 && diff.Removed == 0,
		"approximate":  diff.Approximate,
		"truncated":    truncated,
		"patch":        patch,
	}, nil
}

// renderPatch 输出类 unified diff 的文本。行号对 agent 没有意义（空行已被忽略），
// hunk 之间用 "@@" 分隔。
func renderPatch(from, to webpage.VersionFile, diff webpage.LineDiff, maxLines int) (string, bool) {
	var b strings.Builder
	b.WriteString("--- v" + strconv.Itoa(from.Version) + " " + from.CapturedAt + "\n")
	b.WriteString("+++ v" + strconv.Itoa(to.Version) + " " + to.CapturedAt + "\n")
	if len(diff.Ops) > 0 && diff.Ops[0].Kind != 0 {
		b.WriteString("@@\n")
	}
	written := 0
	for _, op := range diff.Ops {
		if written >= maxLines {
			return b.String(), true
		}
		if op.Kind == 0 {
			b.WriteString("@@\n")
		} else {
			b.WriteByte(op.Kind)
			b.WriteString(op.Text)
			b.WriteByte('\n')
		}
		written++
	}
	return b.String(), false
}

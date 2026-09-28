package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/transfer"
	"github.com/YoooClaw/cli/internal/transfercloud"
	"github.com/spf13/cobra"
)

// transferTimeout 覆盖大包的校验与复制；与插件 CLI 的 30 分钟一致。
const transferTimeout = 30 * time.Minute

// maxIssueRows 是输出里逐条列出的冲突/失败记录上限；完整明细在 report.json。
const maxIssueRows = 50

func newTransferCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "transfer",
		Short: "通过 OSS 任务或本地包迁移数据（与 OpenClaw 插件 ntf transfer 互通）",
	}
	capabilities := &cobra.Command{
		Use:   "capabilities",
		Short: "读取目标端（运行中的 daemon）的迁移能力 🟡",
		Args:  cobra.NoArgs,
		RunE:  run(transferCapabilities),
	}
	capabilities.Flags().String("out", "", "把能力文件保存到指定路径（交给源端 export --target-capabilities）")

	export := &cobra.Command{
		Use:   "export",
		Short: "导出数据包；--via oss 上传并返回迁移 taskId 🟢",
		Args:  cobra.NoArgs,
		RunE:  run(transferExport),
	}
	export.Flags().String("via", "local", "传输方式：oss 上传到云端，local 保存本地")
	export.Flags().String("out", "", "新的输出路径（必须不存在）：以 .tar.gz/.tgz 结尾生成单个压缩包，否则生成包目录")
	export.Flags().Bool("dry-run", false, "只预览，不生成数据包")
	export.Flags().String("include", "", "逗号分隔：notifications,recordings,web-pages,images（缺省前三类）")
	export.Flags().String("from", "", "起始时间（含），必须带时区，如 2026-01-01T00:00:00+08:00")
	export.Flags().String("to", "", "结束时间（不含），必须带时区")
	export.Flags().Bool("with-audio", false, "包含已下载的录音音频")
	export.Flags().Bool("with-images", false, "包含图片")
	export.Flags().Bool("with-html", false, "包含网页原始 HTML 存档")
	export.Flags().String("target-capabilities", "", "目标端 capabilities 文件，导出前预检兼容性")

	imp := &cobra.Command{
		Use:   "import",
		Short: "--task 自动导入；--file 暂存预览；--local + --plan 执行计划 🟡",
		Args:  cobra.NoArgs,
		RunE:  run(transferImport),
	}
	imp.Flags().String("task", "", "迁移 taskId；自动下载、校验、导入，全部成功后删除云端包")
	imp.Flags().String("file", "", "本地数据包（.tar.gz 压缩包或包目录）；只暂存、校验与预览，不合并")
	imp.Flags().Bool("dry-run", false, "--task 只下载并预览；--file 本身只预览")
	imp.Flags().String("local", "", "预览返回的 localTransferId")
	imp.Flags().String("plan", "", "预览返回的 planId")
	imp.Flags().Bool("resume", false, "续跑中断或 PARTIAL 的导入")

	complete := &cobra.Command{Use: "complete", Short: "重试确认已上传的迁移包", Args: cobra.NoArgs, RunE: run(transferComplete)}
	complete.Flags().String("task", "", "迁移 taskId")
	complete.Flags().String("object-key", "", "创建任务时返回的 objectKey")
	remove := &cobra.Command{Use: "delete", Short: "删除本人云端迁移包", Args: cobra.NoArgs, RunE: run(transferDelete)}
	remove.Flags().String("task", "", "迁移 taskId")
	c.AddCommand(capabilities, export, imp, complete, remove)
	return c
}

func transferError(err error) error {
	if code := transfer.ErrorCode(err); code != "" {
		return errs.New("YOOOCLAW_TRANSFER_"+code, err.Error())
	}
	return err
}

// transferRequest 把动作交给运行中的 daemon：导入必须经 daemon 串行写入存储。
func transferRequest(ctx *clictx.Context, req transfer.Request) (map[string]any, error) {
	c, err := daemonProxy(ctx)
	if err != nil {
		return nil, err
	}
	status, body, err := c.WithTimeout(transferTimeout).Request("POST", "/transfer", req)
	if err != nil {
		return nil, err
	}
	m, _ := body.(map[string]any)
	if status == 404 {
		return nil, errs.New("YOOOCLAW_TRANSFER_UNSUPPORTED", "运行中的 daemon 不支持数据迁移（版本过旧）").
			WithHint("执行 yoooclaw daemon restart 让 daemon 升级到当前 CLI 版本")
	}
	if status != 200 {
		if e, ok := m["error"].(map[string]any); ok {
			code, _ := e["code"].(string)
			msg, _ := e["message"].(string)
			if code == "" {
				code = "YOOOCLAW_TRANSFER_FAILED"
			}
			return nil, errs.New(code, msg)
		}
		return nil, errs.Newf("YOOOCLAW_TRANSFER_FAILED", "daemon 返回 %d", status)
	}
	return m, nil
}

// summarize 把逐条的 decisions/results 折叠成计数，只保留冲突与失败的明细。
func summarize(result map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range result {
		out[k] = v
	}
	for _, key := range []string{"decisions", "results"} {
		rows, ok := result[key].([]any)
		if !ok {
			continue
		}
		delete(out, key)
		counts := map[string]int{}
		issues := []any{}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			label, _ := row["action"].(string)
			if label == "" {
				label, _ = row["outcome"].(string)
			}
			counts[label]++
			if (label == transfer.OutcomeConflict || label == transfer.OutcomeFailed) && len(issues) < maxIssueRows {
				issues = append(issues, row)
			}
		}
		out["counts"] = counts
		if len(issues) > 0 {
			out["issues"] = issues
		}
	}
	return out
}

func transferCapabilities(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	result, err := transferRequest(ctx, transfer.Request{Action: "capabilities"})
	if err != nil {
		return nil, err
	}
	if out := flagStr(cmd, "out"); out != "" {
		abs, _ := filepath.Abs(out)
		raw, _ := json.MarshalIndent(result, "", "  ")
		if err := fsutil.WriteAtomic(abs, raw, fsutil.SecretFileMode); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func transferExport(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	via := flagStr(cmd, "via")
	if via != "local" && via != "oss" {
		return nil, errs.New(errs.CodeInvalidArgument, "--via 只支持 local 或 oss")
	}
	if via == "oss" && flagStr(cmd, "out") != "" {
		return nil, errs.New(errs.CodeInvalidArgument, "--via oss 自动打包，无需 --out")
	}
	targetFile := flagStr(cmd, "target-capabilities")
	if targetFile != "" {
		raw, err := os.ReadFile(targetFile)
		if err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "读取 --target-capabilities 失败："+err.Error())
		}
		var caps map[string]any
		if json.Unmarshal(raw, &caps) != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "--target-capabilities 不是合法 JSON")
		}
		if err := transfer.CheckCapabilities(caps); err != nil {
			return nil, transferError(err)
		}
		if via == "oss" || transfer.IsArchivePath(flagStr(cmd, "out")) {
			if err := transfer.CheckArchiveSupport(caps); err != nil {
				return nil, transferError(err)
			}
		}
	}
	dryRun := flagBool(cmd, "dry-run")
	out := flagStr(cmd, "out")
	var cloud *transfercloud.Client
	if via == "oss" && !dryRun {
		cloud = transferCloudClient(ctx)
		if err := cloud.CheckAuth(); err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp("", "yoooclaw-export-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		out = filepath.Join(dir, "migration.tar.gz")
	}
	if !dryRun && out == "" {
		return nil, errs.New("YOOOCLAW_TRANSFER_OUTPUT_REQUIRED", "需要 --out <新路径，如 ./pkg.tar.gz>，或用 --dry-run 预览")
	}
	output := ""
	if !dryRun {
		output, _ = filepath.Abs(out)
	}
	roots := transfer.Roots{
		transfer.TypeNotifications: ctx.Paths.Notifications,
		transfer.TypeRecordings:    ctx.Paths.Recordings,
		transfer.TypeWebPages:      ctx.Paths.WebPages,
		transfer.TypeImages:        ctx.Paths.Images,
	}
	opts := transfer.ExportOptions{
		Include: flagStr(cmd, "include"), From: flagStr(cmd, "from"), To: flagStr(cmd, "to"),
		WithAudio: flagBool(cmd, "with-audio"), WithImages: flagBool(cmd, "with-images"), WithHTML: flagBool(cmd, "with-html"),
	}
	archive := output != "" && transfer.IsArchivePath(output)
	var (
		m            *transfer.Manifest
		archiveBytes int64
		err          error
	)
	if archive {
		m, archiveBytes, err = transfer.ExportArchive(roots, output, opts)
	} else {
		m, err = transfer.Export(roots, output, opts)
	}
	if err != nil {
		return nil, transferError(err)
	}
	counts := map[string]int{}
	var bytes int64
	for _, r := range m.Records {
		counts[r.Type]++
		for _, a := range r.Assets {
			bytes += a.Bytes
		}
	}
	result := map[string]any{
		"packageId": m.PackageID, "records": len(m.Records), "recordsByType": counts, "bytes": bytes,
		"warnings": m.Warnings, "plaintext": true, "targetVerified": targetFile != "",
		"dryRun": dryRun, "capabilities": transfer.Capabilities(),
	}
	if output != "" {
		result["path"] = output
		result["packageFormat"] = "directory"
		if archive {
			result["packageFormat"] = "tar.gz"
			result["archiveBytes"] = archiveBytes
		}
	}
	if cloud != nil {
		uploadCtx, cancel := context.WithTimeout(cmd.Context(), transferTimeout)
		defer cancel()
		receipt, err := cloud.Upload(uploadCtx, output)
		if err != nil {
			return nil, err
		}
		delete(result, "path")
		for k, v := range transferTaskResult(receipt) {
			result[k] = v
		}
	}
	return result, nil
}

func transferImport(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	file, local, plan := flagStr(cmd, "file"), flagStr(cmd, "local"), flagStr(cmd, "plan")
	if task := flagStr(cmd, "task"); task != "" {
		if file != "" || local != "" || plan != "" || flagBool(cmd, "resume") {
			return nil, errs.New(errs.CodeInvalidArgument, "--task 不能与 --file、--local、--plan 或 --resume 同用")
		}
		return transferImportTask(ctx, cmd, task)
	}
	if file != "" && local != "" {
		return nil, errs.New("YOOOCLAW_TRANSFER_FILE_OR_LOCAL", "--file 与 --local 只能二选一")
	}
	var req transfer.Request
	if file != "" {
		// --file 永远只到预览为止；计划属于它自己的 staging 副本，合并走 --local。
		if plan != "" {
			return nil, errs.New("YOOOCLAW_TRANSFER_USE_LOCAL_PLAN", "执行计划请用预览返回的 --local 与 --plan")
		}
		abs, _ := filepath.Abs(file)
		req = transfer.Request{Action: "preview", File: abs}
	} else {
		if local == "" || plan == "" || flagBool(cmd, "dry-run") {
			return nil, errs.New("YOOOCLAW_TRANSFER_LOCAL_AND_PLAN_REQUIRED", "需要 --file <包文件或目录> 预览，或 --local 与 --plan 执行计划")
		}
		req = transfer.Request{Action: "import", LocalTransferID: local, PlanID: plan, Resume: flagBool(cmd, "resume")}
	}
	result, err := transferRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return summarize(result), nil
}

func transferCloudClient(ctx *clictx.Context) *transfercloud.Client {
	return transfercloud.New(creds.ResolveAPIKey().Value, cloudHost(ctx))
}
func transferTaskResult(receipt *transfercloud.Receipt) map[string]any {
	return map[string]any{"taskId": receipt.TaskID, "fileSize": int64(receipt.FileSize), "status": receipt.Status, "importPrompt": "帮我导入这份迁移数据：" + receipt.TaskID, "nextCommand": "yoooclaw transfer import --task " + receipt.TaskID}
}
func transferComplete(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	reqCtx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
	defer cancel()
	receipt, err := transferCloudClient(ctx).Complete(reqCtx, flagStr(cmd, "task"), flagStr(cmd, "object-key"))
	if err != nil {
		return nil, err
	}
	return transferTaskResult(receipt), nil
}
func transferDelete(ctx *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	reqCtx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
	defer cancel()
	id := flagStr(cmd, "task")
	if err := transferCloudClient(ctx).Delete(reqCtx, id); err != nil {
		return nil, err
	}
	return map[string]any{"taskId": id, "status": "DELETED"}, nil
}
func transferImportTask(ctx *clictx.Context, cmd *cobra.Command, id string) (any, error) {
	if !transfercloud.ValidTaskID(id) {
		return nil, errs.New("YOOOCLAW_TRANSFER_INVALID_TASK", "迁移 taskId 无效")
	}
	cloud := transferCloudClient(ctx)
	if err := cloud.CheckAuth(); err != nil {
		return nil, err
	}
	// Refuse older daemons before download: they ignore cloudTaskId and cannot
	// guarantee cleanup for a later --local/--plan import.
	caps, err := transferRequest(ctx, transfer.Request{Action: "capabilities"})
	if err != nil {
		return nil, err
	}
	transports, _ := caps["transport"].([]any)
	supported := false
	for _, v := range transports {
		if v == "oss-task" {
			supported = true
		}
	}
	if !supported {
		return nil, errs.New("YOOOCLAW_TRANSFER_UNSUPPORTED", "运行中的 daemon 不支持云端迁移，请执行 yoooclaw daemon restart")
	}
	dir, err := os.MkdirTemp("", "yoooclaw-import-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	downloadCtx, cancel := context.WithTimeout(cmd.Context(), transferTimeout)
	defer cancel()
	path := filepath.Join(dir, "migration.tar.gz")
	if err = cloud.Download(downloadCtx, id, path); err != nil {
		return nil, err
	}
	preview, err := transferRequest(ctx, transfer.Request{Action: "preview", File: path, CloudTaskID: id})
	if err != nil {
		return nil, err
	}
	if flagBool(cmd, "dry-run") {
		return summarize(preview), nil
	}
	local, _ := preview["localTransferId"].(string)
	plan, _ := preview["planId"].(string)
	if local == "" || plan == "" {
		return nil, errs.New("YOOOCLAW_TRANSFER_PLAN_REQUIRED", "daemon 未返回有效的导入计划")
	}
	result, err := transferRequest(ctx, transfer.Request{Action: "import", LocalTransferID: local, PlanID: plan})
	if err != nil {
		return nil, err
	}
	result["taskId"] = id
	if result["state"] != "SUCCEEDED" {
		result["nextCommand"] = "yoooclaw transfer import --local " + local + " --plan " + plan + " --resume"
	}
	return summarize(result), nil
}

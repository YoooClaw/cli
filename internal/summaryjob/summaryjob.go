// Package summaryjob 实现分片通知总结任务：把一段时间/筛选范围内的通知切成
// 固定大小的分片，逐片交给模型总结，最后合并为一份 Markdown 结果。
//
// 任务态落盘在 <notificationsDir>/.summaries/jobs/<id>/ 下：
//   - meta.json            任务元数据与分片索引
//   - chunks/<chunkId>.json 每个分片的 compact 通知
//   - summaries/<chunkId>.md 每个分片提交的摘要
//   - result.md            合并后的最终结果
//
// 对齐 phone-notifications 的 ntf summary-job 实现。
package summaryjob

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/notif"
)

// 默认参数（对齐 TS DEFAULT_*）。
const (
	DefaultJobLimit     = 1000
	DefaultChunkSize    = 150
	DefaultMaxContent   = 120
	DefaultRunMaxChunks = 20
)

const (
	summaryRoot  = ".summaries"
	jobsDir      = "jobs"
	chunksDir    = "chunks"
	summariesDir = "summaries"
	resultFile   = "result.md"
	cmdPrefix    = "yoooclaw notification summary-job"
)

var attentionKeywords = []string{
	"待办", "需要", "麻烦", "确认", "回复", "审批", "开会", "会议", "安排",
	"截止", "紧急", "异常", "失败", "风险",
	"todo", "deadline", "urgent", "error", "failed",
}

// QueryMeta 是任务记录的查询条件（原始字符串，用于展示与复现）。
type QueryMeta struct {
	From             string `json:"from,omitempty"`
	To               string `json:"to,omitempty"`
	App              string `json:"app,omitempty"`
	Sender           string `json:"sender,omitempty"`
	ConversationType string `json:"conversationType,omitempty"`
	Keyword          string `json:"keyword,omitempty"`
	Limit            int    `json:"limit"`
}

type rangeInfo struct {
	Newest *string `json:"newest"`
	Oldest *string `json:"oldest"`
}

type scanInfo struct {
	DaysScanned       int   `json:"daysScanned"`
	ItemsScanned      int   `json:"itemsScanned"`
	Matched           int   `json:"matched"`
	StoppedAfterLimit bool  `json:"stoppedAfterLimit"`
	ElapsedMs         int64 `json:"elapsedMs"`
}

type chunk struct {
	ID          string    `json:"id"`
	Index       int       `json:"index"`
	Start       int       `json:"start"`
	End         int       `json:"end"`
	Count       int       `json:"count"`
	Status      string    `json:"status"` // pending | in_progress | done
	Range       rangeInfo `json:"range"`
	ClaimedAt   string    `json:"claimedAt,omitempty"`
	CompletedAt string    `json:"completedAt,omitempty"`
	SummaryFile string    `json:"summaryFile,omitempty"`
}

type meta struct {
	SchemaVersion int       `json:"schemaVersion"`
	ID            string    `json:"id"`
	Status        string    `json:"status"` // pending | in_progress | ready | complete | cancelled
	CreatedAt     string    `json:"createdAt"`
	UpdatedAt     string    `json:"updatedAt"`
	Query         QueryMeta `json:"query"`
	ChunkSize     int       `json:"chunkSize"`
	MaxContent    int       `json:"maxContent"`
	Total         int       `json:"total"`
	Range         rangeInfo `json:"range"`
	Scan          scanInfo  `json:"scan"`
	Chunks        []chunk   `json:"chunks"`
	ResultFile    string    `json:"resultFile,omitempty"`
}

type compactNotification struct {
	AppName          string `json:"appName"`
	AppDisplayName   string `json:"appDisplayName,omitempty"`
	Title            string `json:"title"`
	Content          string `json:"content"`
	Timestamp        string `json:"timestamp"`
	SenderName       string `json:"senderName,omitempty"`
	ConversationType string `json:"conversationType,omitempty"`
	ConversationName string `json:"conversationName,omitempty"`
}

type chunkFile struct {
	ID            string                `json:"id"`
	Index         int                   `json:"index"`
	Start         int                   `json:"start"`
	End           int                   `json:"end"`
	Notifications []compactNotification `json:"notifications"`
}

// ── 工具函数 ──

func nowIso() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

var jobIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)

func createJobID() string {
	stamp := time.Now().UTC().Format("20060102150405")
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return stamp + "-" + hex.EncodeToString(b)
}

func assertJobID(id string) error {
	if !jobIDRE.MatchString(id) {
		return errs.New(errs.CodeInvalidArgument, "summary job id 不合法")
	}
	return nil
}

// truncate 按 rune 截断，超长时以 … 收尾（对齐 TS 语义但按字符而非 UTF-16 单元）。
func truncate(value string, maxLen int) string {
	r := []rune(value)
	if len(r) <= maxLen {
		return value
	}
	if maxLen <= 0 {
		return ""
	}
	return string(r[:maxLen-1]) + "…"
}

func strPtr(s string) *string { return &s }

func compact(item notif.StoredNotification, maxContent int) compactNotification {
	return compactNotification{
		AppName:          item.AppName,
		AppDisplayName:   item.AppDisplayName,
		Title:            truncate(item.Title, maxContent),
		Content:          truncate(item.Content, maxContent),
		Timestamp:        item.Timestamp,
		SenderName:       item.SenderName,
		ConversationType: item.ConversationType,
		ConversationName: item.ConversationName,
	}
}

func notificationApp(item compactNotification) string {
	if v := strings.TrimSpace(item.AppDisplayName); v != "" {
		return v
	}
	if v := strings.TrimSpace(item.AppName); v != "" {
		return v
	}
	return "(unknown)"
}

func notificationSender(item compactNotification) string {
	if v := strings.TrimSpace(item.SenderName); v != "" {
		return v
	}
	if v := strings.TrimSpace(item.ConversationName); v != "" {
		return v
	}
	if v := strings.TrimSpace(item.Title); v != "" {
		return v
	}
	return "(unknown)"
}

// ── 路径 ──

func jobsRootDir(notificationsDir string) string {
	return filepath.Join(notificationsDir, summaryRoot, jobsDir)
}

func jobDir(notificationsDir, id string) string {
	return filepath.Join(jobsRootDir(notificationsDir), id)
}

func metaPath(dir string) string { return filepath.Join(dir, "meta.json") }

func chunkPath(dir, chunkID string) string {
	return filepath.Join(dir, chunksDir, chunkID+".json")
}

// ── 元数据读写 ──

func readMeta(notificationsDir, id string) (*meta, error) {
	if err := assertJobID(id); err != nil {
		return nil, err
	}
	dir := jobDir(notificationsDir, id)
	var m meta
	exists, err := fsutil.ReadJSON(metaPath(dir), &m)
	if err != nil {
		return nil, errs.Newf(errs.CodeInvalidArgument, "summary job 元数据损坏: %s", id)
	}
	if !exists {
		return nil, errs.Newf(errs.CodeNotFound, "summary job 不存在: %s", id)
	}
	if m.SchemaVersion != 1 || m.ID == "" {
		return nil, errs.Newf(errs.CodeInvalidArgument, "summary job 元数据损坏: %s", id)
	}
	return &m, nil
}

func saveMeta(notificationsDir string, m *meta) error {
	m.UpdatedAt = nowIso()
	return fsutil.WriteJSON(metaPath(jobDir(notificationsDir, m.ID)), m, fsutil.ConfigFileMode)
}

func readChunkNotifications(dir, chunkID string) ([]compactNotification, error) {
	var cf chunkFile
	exists, err := fsutil.ReadJSON(chunkPath(dir, chunkID), &cf)
	if err != nil || !exists {
		return nil, errs.Newf(errs.CodeInvalidArgument, "summary job 分片损坏: %s", chunkID)
	}
	return cf.Notifications, nil
}

// ── 状态计算 ──

func refreshStatus(m *meta) string {
	if m.Status == "cancelled" || m.Status == "complete" {
		return m.Status
	}
	allDone := true
	anyInProgress := false
	for _, c := range m.Chunks {
		if c.Status != "done" {
			allDone = false
		}
		if c.Status == "in_progress" {
			anyInProgress = true
		}
	}
	if allDone { // 含空分片：对齐 TS .every() 对空数组返回 true 的语义
		return "ready"
	}
	if anyInProgress {
		return "in_progress"
	}
	return "pending"
}

func progressFor(m *meta) map[string]any {
	doneChunks := 0
	doneNotifications := 0
	for _, c := range m.Chunks {
		if c.Status == "done" {
			doneChunks++
			doneNotifications += c.Count
		}
	}
	return map[string]any{
		"totalChunks":        len(m.Chunks),
		"doneChunks":         doneChunks,
		"totalNotifications": m.Total,
		"doneNotifications":  doneNotifications,
		"remainingChunks":    len(m.Chunks) - doneChunks,
	}
}

func nilStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func toPublicStatus(m *meta) map[string]any {
	chunks := make([]any, 0, len(m.Chunks))
	for _, c := range m.Chunks {
		chunks = append(chunks, map[string]any{
			"id":          c.ID,
			"index":       c.Index,
			"count":       c.Count,
			"status":      c.Status,
			"range":       c.Range,
			"summaryFile": nilStr(c.SummaryFile),
		})
	}
	return map[string]any{
		"ok":         true,
		"id":         m.ID,
		"status":     m.Status,
		"createdAt":  m.CreatedAt,
		"updatedAt":  m.UpdatedAt,
		"query":      m.Query,
		"total":      m.Total,
		"chunkSize":  m.ChunkSize,
		"range":      m.Range,
		"scan":       m.Scan,
		"progress":   progressFor(m),
		"resultFile": nilStr(m.ResultFile),
		"chunks":     chunks,
	}
}

func nextCommand(id string) string   { return cmdPrefix + " next " + id }
func resultCommand(id string) string { return cmdPrefix + " result " + id }
func commitCommand(id, chunkID string) string {
	return cmdPrefix + " commit " + id + " --chunk-id " + chunkID + " --summary-file <path>"
}

// ── 命令实现 ──

// CreateParams 是创建任务的入参。Query 用于实际扫描，Meta 用于记录展示。
type CreateParams struct {
	Query      notif.QueryOptions
	Meta       QueryMeta
	ChunkSize  int
	MaxContent int
}

// Create 扫描匹配通知并切片，落盘任务态。
func Create(notificationsDir string, p CreateParams) (map[string]any, error) {
	start := time.Now()
	notifications, stats := notif.QueryWithStats(notificationsDir, p.Query)
	elapsed := time.Since(start).Milliseconds()

	id := createJobID()
	dir := jobDir(notificationsDir, id)
	if err := fsutil.EnsureDir(filepath.Join(dir, chunksDir), fsutil.DirMode); err != nil {
		return nil, err
	}
	if err := fsutil.EnsureDir(filepath.Join(dir, summariesDir), fsutil.DirMode); err != nil {
		return nil, err
	}

	chunks := make([]chunk, 0)
	for s := 0; s < len(notifications); s += p.ChunkSize {
		e := s + p.ChunkSize
		if e > len(notifications) {
			e = len(notifications)
		}
		raw := notifications[s:e]
		compacted := make([]compactNotification, 0, len(raw))
		for _, item := range raw {
			compacted = append(compacted, compact(item, p.MaxContent))
		}
		index := len(chunks)
		chunkID := pad4(index + 1)
		end := s + len(compacted) - 1
		if err := fsutil.WriteJSON(chunkPath(dir, chunkID), chunkFile{
			ID: chunkID, Index: index, Start: s, End: end, Notifications: compacted,
		}, fsutil.ConfigFileMode); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk{
			ID: chunkID, Index: index, Start: s, End: end, Count: len(compacted),
			Status: "pending", Range: rangeFor(compacted),
		})
	}

	createdAt := nowIso()
	status := "pending"
	if len(chunks) == 0 {
		status = "ready"
	}
	m := &meta{
		SchemaVersion: 1, ID: id, Status: status, CreatedAt: createdAt, UpdatedAt: createdAt,
		Query: p.Meta, ChunkSize: p.ChunkSize, MaxContent: p.MaxContent,
		Total: len(notifications), Range: rangeForStored(notifications),
		Scan: scanInfo{
			DaysScanned: stats.DaysScanned, ItemsScanned: stats.ItemsScanned,
			Matched: stats.Matched, StoppedAfterLimit: stats.StoppedAfterLimit, ElapsedMs: elapsed,
		},
		Chunks: chunks,
	}
	if err := fsutil.WriteJSON(metaPath(dir), m, fsutil.ConfigFileMode); err != nil {
		return nil, err
	}

	out := toPublicStatus(m)
	out["nextCommand"] = nextCommand(id)
	out["resultCommand"] = resultCommand(id)
	return out, nil
}

// Status 刷新并返回任务状态。
func Status(notificationsDir, id string) (map[string]any, error) {
	m, err := readMeta(notificationsDir, id)
	if err != nil {
		return nil, err
	}
	m.Status = refreshStatus(m)
	if err := saveMeta(notificationsDir, m); err != nil {
		return nil, err
	}
	return toPublicStatus(m), nil
}

// Next 领取下一个待总结分片（或重试 in_progress 分片），返回其通知与 commit 命令。
func Next(notificationsDir, id string) (map[string]any, error) {
	m, err := readMeta(notificationsDir, id)
	if err != nil {
		return nil, err
	}
	if m.Status == "cancelled" {
		return nil, errs.Newf(errs.CodeInvalidArgument, "summary job 已取消: %s", id)
	}
	dir := jobDir(notificationsDir, id)

	idx := indexOfStatus(m.Chunks, "in_progress")
	if idx < 0 {
		if p := indexOfStatus(m.Chunks, "pending"); p >= 0 {
			m.Chunks[p].Status = "in_progress"
			m.Chunks[p].ClaimedAt = nowIso()
			idx = p
		}
	}

	if idx < 0 {
		m.Status = refreshStatus(m)
		if err := saveMeta(notificationsDir, m); err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "id": id, "done": true, "status": m.Status,
			"progress": progressFor(m), "resultCommand": resultCommand(id),
		}, nil
	}

	c := m.Chunks[idx]
	notifications, err := readChunkNotifications(dir, c.ID)
	if err != nil {
		return nil, err
	}
	m.Status = refreshStatus(m)
	if err := saveMeta(notificationsDir, m); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "id": id, "done": false, "status": m.Status,
		"chunk": map[string]any{
			"id": c.ID, "index": c.Index, "start": c.Start, "end": c.End,
			"count": c.Count, "range": c.Range,
		},
		"progress":      progressFor(m),
		"commitCommand": commitCommand(id, c.ID),
		"notifications": notifications,
	}, nil
}

// Commit 提交某分片的摘要文本并标记完成；全部完成后自动生成结果。
func Commit(notificationsDir, id, chunkID, summaryText string) (map[string]any, error) {
	m, err := readMeta(notificationsDir, id)
	if err != nil {
		return nil, err
	}
	if m.Status == "cancelled" {
		return nil, errs.Newf(errs.CodeInvalidArgument, "summary job 已取消: %s", id)
	}
	idx := indexOfID(m.Chunks, chunkID)
	if idx < 0 {
		return nil, errs.Newf(errs.CodeNotFound, "summary job 分片不存在: %s", chunkID)
	}
	text := strings.TrimRight(summaryText, "\n") + "\n"
	if strings.TrimSpace(text) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "分片摘要不能为空")
	}
	if err := writeChunkSummary(notificationsDir, m, idx, text); err != nil {
		return nil, err
	}
	m.Status = refreshStatus(m)
	if err := saveMeta(notificationsDir, m); err != nil {
		return nil, err
	}

	out := map[string]any{
		"ok": true, "id": id, "chunkId": chunkID, "status": m.Status, "progress": progressFor(m),
	}
	if m.Status == "ready" {
		out["nextCommand"] = nil
		out["resultCommand"] = resultCommand(id)
	} else {
		out["nextCommand"] = nextCommand(id)
		out["resultCommand"] = nil
	}
	return out, nil
}

// Run 自动用抽取式摘要处理待总结分片（无需模型），完成后生成结果。
func Run(notificationsDir, id string, maxChunks int, includeResult bool) (map[string]any, error) {
	m, err := readMeta(notificationsDir, id)
	if err != nil {
		return nil, err
	}
	if m.Status == "cancelled" {
		return nil, errs.Newf(errs.CodeInvalidArgument, "summary job 已取消: %s", id)
	}
	dir := jobDir(notificationsDir, id)
	processed := make([]any, 0)
	for i := range m.Chunks {
		if len(processed) >= maxChunks {
			break
		}
		if m.Chunks[i].Status == "done" {
			continue
		}
		m.Chunks[i].Status = "in_progress"
		m.Chunks[i].ClaimedAt = nowIso()
		notifications, err := readChunkNotifications(dir, m.Chunks[i].ID)
		if err != nil {
			return nil, err
		}
		summary := buildExtractiveChunkSummary(m.Chunks[i], notifications)
		if err := writeChunkSummary(notificationsDir, m, i, summary); err != nil {
			return nil, err
		}
		processed = append(processed, map[string]any{"chunkId": m.Chunks[i].ID, "count": m.Chunks[i].Count})
	}

	markdown, err := finalizeResultIfReady(notificationsDir, m)
	if err != nil {
		return nil, err
	}
	if markdown == "" {
		m.Status = refreshStatus(m)
	}
	if err := saveMeta(notificationsDir, m); err != nil {
		return nil, err
	}

	out := map[string]any{
		"ok": true, "id": id, "status": m.Status, "processed": processed,
		"progress": progressFor(m), "resultFile": nilStr(m.ResultFile),
	}
	if m.Status == "complete" {
		out["nextCommand"] = nil
		out["resultCommand"] = resultCommand(id)
	} else {
		out["nextCommand"] = cmdPrefix + " run " + id
		out["resultCommand"] = nil
	}
	if includeResult && markdown != "" {
		out["markdown"] = markdown
	}
	return out, nil
}

// Result 合并已提交的分片摘要并返回结果（未就绪时返回 done=false）。
func Result(notificationsDir, id string) (map[string]any, error) {
	m, err := readMeta(notificationsDir, id)
	if err != nil {
		return nil, err
	}
	m.Status = refreshStatus(m)
	if m.Status != "ready" && m.Status != "complete" {
		if err := saveMeta(notificationsDir, m); err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "id": id, "done": false, "status": m.Status,
			"progress": progressFor(m), "nextCommand": nextCommand(id),
		}, nil
	}

	markdown, err := finalizeResultIfReady(notificationsDir, m)
	if err != nil {
		return nil, err
	}
	if markdown == "" {
		markdown, err = buildResultMarkdown(notificationsDir, m)
		if err != nil {
			return nil, err
		}
	}
	if err := saveMeta(notificationsDir, m); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "id": id, "done": true, "status": m.Status,
		"resultFile": resultFile, "markdown": markdown,
	}, nil
}

// Cancel 取消任务（保留已落盘的分片与摘要）。
func Cancel(notificationsDir, id string) (map[string]any, error) {
	m, err := readMeta(notificationsDir, id)
	if err != nil {
		return nil, err
	}
	m.Status = "cancelled"
	if err := saveMeta(notificationsDir, m); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id, "status": m.Status, "progress": progressFor(m)}, nil
}

// ── 内部辅助 ──

func writeChunkSummary(notificationsDir string, m *meta, idx int, text string) error {
	dir := jobDir(notificationsDir, m.ID)
	rel := filepath.Join(summariesDir, m.Chunks[idx].ID+".md")
	if err := fsutil.WriteAtomic(filepath.Join(dir, rel), []byte(text), fsutil.ConfigFileMode); err != nil {
		return err
	}
	m.Chunks[idx].Status = "done"
	m.Chunks[idx].CompletedAt = nowIso()
	m.Chunks[idx].SummaryFile = rel
	return nil
}

// finalizeResultIfReady 在任务就绪/完成时写出 result.md 并返回内容，否则返回空串。
func finalizeResultIfReady(notificationsDir string, m *meta) (string, error) {
	m.Status = refreshStatus(m)
	if m.Status != "ready" && m.Status != "complete" {
		return "", nil
	}
	markdown, err := buildResultMarkdown(notificationsDir, m)
	if err != nil {
		return "", err
	}
	if err := fsutil.WriteAtomic(filepath.Join(jobDir(notificationsDir, m.ID), resultFile), []byte(markdown), fsutil.ConfigFileMode); err != nil {
		return "", err
	}
	m.Status = "complete"
	m.ResultFile = resultFile
	return markdown, nil
}

func buildResultMarkdown(notificationsDir string, m *meta) (string, error) {
	dir := jobDir(notificationsDir, m.ID)
	var b strings.Builder
	b.WriteString("# 通知总结任务 " + m.ID + "\n\n")
	b.WriteString("- 通知数: " + strconv.Itoa(m.Total) + "\n")
	b.WriteString("- 时间范围: " + dash(m.Range.Oldest) + " 至 " + dash(m.Range.Newest) + "\n")
	b.WriteString("- 分片数: " + strconv.Itoa(len(m.Chunks)) + "\n\n")
	b.WriteString("## 分片摘要\n\n")
	for _, c := range m.Chunks {
		b.WriteString("### " + c.ID + " · " + strconv.Itoa(c.Count) + " 条 · " + dash(c.Range.Oldest) + " 至 " + dash(c.Range.Newest) + "\n\n")
		if c.SummaryFile == "" {
			b.WriteString("(未提交摘要)\n\n")
			continue
		}
		raw, err := readFileTrim(filepath.Join(dir, c.SummaryFile))
		if err != nil {
			return "", err
		}
		b.WriteString(raw + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func buildExtractiveChunkSummary(c chunk, notifications []compactNotification) string {
	byApp := map[string]int{}
	bySender := map[string]int{}
	for _, item := range notifications {
		byApp[notificationApp(item)]++
		bySender[notificationSender(item)]++
	}

	var latest []string
	for i, item := range notifications {
		if i >= 5 {
			break
		}
		latest = append(latest, compactLine(item))
	}
	var attention []string
	for _, item := range notifications {
		if len(attention) >= 5 {
			break
		}
		if isAttentionCandidate(item) {
			attention = append(attention, compactLine(item))
		}
	}

	var b strings.Builder
	b.WriteString("## 分片 " + c.ID + "\n\n")
	b.WriteString("- 数量: " + strconv.Itoa(c.Count) + "\n")
	b.WriteString("- 时间范围: " + dash(c.Range.Oldest) + " 至 " + dash(c.Range.Newest) + "\n")
	b.WriteString("- 主要 App: " + formatTopCounts(byApp, 5) + "\n")
	b.WriteString("- 主要发送人/会话: " + formatTopCounts(bySender, 8) + "\n\n")
	b.WriteString("### 可能需要关注\n")
	if len(attention) > 0 {
		for _, line := range attention {
			b.WriteString("- " + line + "\n")
		}
	} else {
		b.WriteString("- 无明显待办、风险或异常样例\n")
	}
	b.WriteString("\n### 最近样例\n")
	if len(latest) > 0 {
		for _, line := range latest {
			b.WriteString("- " + line + "\n")
		}
	} else {
		b.WriteString("- 无\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func compactLine(item compactNotification) string {
	return "[" + item.Timestamp + "] " + notificationApp(item) + " · " + notificationSender(item) + ": " + item.Content
}

func isAttentionCandidate(item compactNotification) bool {
	text := strings.ToLower(item.Title + "\n" + item.Content)
	for _, kw := range attentionKeywords {
		if strings.Contains(text, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// formatTopCounts 取计数前 limit 名，按「计数降序、键升序」排序后拼成 "k1 c1；k2 c2"。
func formatTopCounts(counts map[string]int, limit int) string {
	type kc struct {
		key   string
		count int
	}
	rows := make([]kc, 0, len(counts))
	for k, v := range counts {
		rows = append(rows, kc{k, v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].key < rows[j].key
	})
	if limit < len(rows) {
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, r.key+" "+strconv.Itoa(r.count))
	}
	return strings.Join(parts, "；")
}

func rangeFor(items []compactNotification) rangeInfo {
	if len(items) == 0 {
		return rangeInfo{}
	}
	return rangeInfo{Newest: strPtr(items[0].Timestamp), Oldest: strPtr(items[len(items)-1].Timestamp)}
}

func rangeForStored(items []notif.StoredNotification) rangeInfo {
	if len(items) == 0 {
		return rangeInfo{}
	}
	return rangeInfo{Newest: strPtr(items[0].Timestamp), Oldest: strPtr(items[len(items)-1].Timestamp)}
}

func indexOfStatus(chunks []chunk, status string) int {
	for i := range chunks {
		if chunks[i].Status == status {
			return i
		}
	}
	return -1
}

func indexOfID(chunks []chunk, id string) int {
	for i := range chunks {
		if chunks[i].ID == id {
			return i
		}
	}
	return -1
}

func pad4(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func dash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func readFileTrim(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errs.Newf(errs.CodeNotFound, "无法读取分片摘要: %s", path)
	}
	return strings.TrimSpace(string(raw)), nil
}

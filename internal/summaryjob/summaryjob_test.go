package summaryjob

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/notif"
)

// writeNotifs 在 dir/<dateKey>.json 写入 n 条通知。
func writeNotifs(t *testing.T, dir, dateKey string, n int) {
	t.Helper()
	items := make([]notif.StoredNotification, n)
	for i := range items {
		items[i] = notif.StoredNotification{
			AppName: "微信", AppDisplayName: "微信",
			Title: "标题", Content: "需要确认开会", SenderName: "张三",
			Timestamp: dateKey + "T10:00:00+08:00",
		}
	}
	raw, _ := json.Marshal(items)
	if err := os.WriteFile(filepath.Join(dir, dateKey+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func createJob(t *testing.T, dir string, chunkSize int) string {
	t.Helper()
	opts, err := notif.BuildQueryOptions(notif.RawQueryOpts{}, DefaultJobLimit)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Create(dir, CreateParams{Query: opts, Meta: QueryMeta{Limit: opts.Limit}, ChunkSize: chunkSize, MaxContent: DefaultMaxContent})
	if err != nil {
		t.Fatal(err)
	}
	return res["id"].(string)
}

func TestCreateChunking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeNotifs(t, dir, "2026-06-16", 7)
	res, err := Create(dir, mustOpts(t))
	if err != nil {
		t.Fatal(err)
	}
	if res["total"].(int) != 7 {
		t.Errorf("total should be 7: %+v", res["total"])
	}
	chunks := res["chunks"].([]any)
	if len(chunks) != 3 { // 3+3+1
		t.Errorf("expected 3 chunks: %d", len(chunks))
	}
	if res["status"] != "pending" {
		t.Errorf("status should be pending: %v", res["status"])
	}
}

func mustOpts(t *testing.T) CreateParams {
	t.Helper()
	opts, err := notif.BuildQueryOptions(notif.RawQueryOpts{}, DefaultJobLimit)
	if err != nil {
		t.Fatal(err)
	}
	return CreateParams{Query: opts, Meta: QueryMeta{Limit: opts.Limit}, ChunkSize: 3, MaxContent: DefaultMaxContent}
}

func TestCreateEmptyIsReady(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := createJob(t, dir, 3)
	st, err := Status(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if st["status"] != "ready" {
		t.Errorf("empty job should be ready: %v", st["status"])
	}
}

func TestLifecycleNextCommitResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeNotifs(t, dir, "2026-06-16", 5)
	id := createJob(t, dir, 2) // 2+2+1 = 3 chunks

	seen := map[string]bool{}
	for {
		res, err := Next(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		if res["done"].(bool) {
			break
		}
		c := res["chunk"].(map[string]any)
		chunkID := c["id"].(string)
		if seen[chunkID] {
			t.Fatalf("next returned same chunk twice without commit: %s", chunkID)
		}
		seen[chunkID] = true
		if _, err := Commit(dir, id, chunkID, "摘要 "+chunkID); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 {
		t.Errorf("should have processed 3 chunks: %d", len(seen))
	}

	res, err := Result(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if !res["done"].(bool) || res["status"] != "complete" {
		t.Errorf("result should be complete: %+v", res)
	}
	md := res["markdown"].(string)
	if !strings.Contains(md, "摘要 0001") || !strings.Contains(md, "通知数: 5") {
		t.Errorf("markdown missing content: %s", md)
	}
	// result.md 已落盘
	if _, err := os.Stat(filepath.Join(dir, summaryRoot, jobsDir, id, resultFile)); err != nil {
		t.Errorf("result.md not written: %v", err)
	}
}

func TestRunAutoExtractive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeNotifs(t, dir, "2026-06-16", 5)
	id := createJob(t, dir, 2)

	res, err := Run(dir, id, DefaultRunMaxChunks, true)
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "complete" {
		t.Errorf("run should complete: %v", res["status"])
	}
	if len(res["processed"].([]any)) != 3 {
		t.Errorf("should process 3 chunks: %+v", res["processed"])
	}
	md := res["markdown"].(string)
	if !strings.Contains(md, "可能需要关注") {
		t.Errorf("extractive summary missing attention section: %s", md)
	}
}

func TestRunMaxChunks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeNotifs(t, dir, "2026-06-16", 6)
	id := createJob(t, dir, 2) // 3 chunks

	res, err := Run(dir, id, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] == "complete" {
		t.Errorf("should not complete after 1 of 3 chunks: %v", res["status"])
	}
	if len(res["processed"].([]any)) != 1 {
		t.Errorf("should process exactly 1 chunk: %+v", res["processed"])
	}
	// 再跑一次继续处理
	if _, err := Run(dir, id, DefaultRunMaxChunks, false); err != nil {
		t.Fatal(err)
	}
	st, _ := Status(dir, id)
	if st["status"] != "complete" {
		t.Errorf("should complete after second run: %v", st["status"])
	}
}

func TestCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeNotifs(t, dir, "2026-06-16", 3)
	id := createJob(t, dir, 2)

	if _, err := Cancel(dir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := Next(dir, id); err == nil {
		t.Error("next on cancelled job should error")
	}
	if _, err := Commit(dir, id, "0001", "x"); err == nil {
		t.Error("commit on cancelled job should error")
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Status(dir, "missing"); err == nil {
		t.Error("missing job should error")
	}
	if _, err := Status(dir, "bad id with spaces"); err == nil {
		t.Error("invalid job id should error")
	}
	writeNotifs(t, dir, "2026-06-16", 3)
	id := createJob(t, dir, 2)
	if _, err := Commit(dir, id, "9999", "x"); err == nil {
		t.Error("unknown chunk should error")
	}
	if _, err := Commit(dir, id, "0001", "   \n  "); err == nil {
		t.Error("empty summary should error")
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("short string unchanged: %q", got)
	}
	if got := truncate("你好世界朋友", 3); got != "你好…" {
		t.Errorf("rune truncate wrong: %q", got)
	}
}

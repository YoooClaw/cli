package notif

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/testutil"
)

func newTestStorage(t *testing.T, dir string) *Storage {
	t.Helper()
	s := NewStorage(dir, PluginConfig{}, testutil.Logger{T: t})
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

func notifAt(id string, ts time.Time, body string) RawNotification {
	return RawNotification{
		ID: id, App: "com.tencent.xin", Title: "老板",
		Body: body, Timestamp: ts.Format(time.RFC3339),
	}
}

// 一批通知里同一天只应落盘一次，而不是每条都覆写整个当天数组。
func TestIngestWritesEachDayOnce(t *testing.T) {
	dir := t.TempDir()
	s := newTestStorage(t, dir)

	day1 := time.Date(2026, 3, 23, 9, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)

	var items []RawNotification
	for i := 0; i < 50; i++ {
		items = append(items, notifAt(fmt.Sprintf("a-%d", i), day1.Add(time.Duration(i)*time.Second), fmt.Sprintf("d1-%d", i)))
		items = append(items, notifAt(fmt.Sprintf("b-%d", i), day2.Add(time.Duration(i)*time.Second), fmt.Sprintf("d2-%d", i)))
	}

	res := s.Ingest(items, "probe")
	if res.Ingested != 100 || res.Failed != 0 {
		t.Fatalf("ingested=%d failed=%d, 期望 100/0", res.Ingested, res.Failed)
	}

	for _, day := range []time.Time{day1, day2} {
		key := day.Format("2006-01-02")
		entries, err := readStoredFile(filepath.Join(dir, key+".json"))
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if len(entries) != 50 {
			t.Fatalf("%s 落盘 %d 条，期望 50", key, len(entries))
		}
	}
	if len(res.Inserted) != 100 {
		t.Fatalf("Inserted=%d，期望 100", len(res.Inserted))
	}
	// Inserted 必须保持入参顺序（跨天交替），而不是按天分组后的顺序。
	if res.Inserted[0].Content != "d1-0" || res.Inserted[1].Content != "d2-0" {
		t.Fatalf("Inserted 顺序被打乱: %q, %q", res.Inserted[0].Content, res.Inserted[1].Content)
	}
}

// 半截 JSON（旧版非原子写被中断的产物）不能被当成空的一天静默覆盖。
func TestTruncatedDayFileIsQuarantinedNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dayKey := now.Format("2006-01-02")
	dayFile := filepath.Join(dir, dayKey+".json")

	s := newTestStorage(t, dir)
	original := []RawNotification{
		notifAt("a", now, "1"), notifAt("b", now, "2"), notifAt("c", now, "3"),
	}
	s.Ingest(original, "probe")

	raw, err := os.ReadFile(dayFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dayFile, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}

	// 新进程：缓存是空的，重新加载当天。
	s2 := newTestStorage(t, dir)
	res := s2.Ingest(original, "probe")

	// 损坏的字节必须被留下来，而不是被覆盖掉。
	matches, _ := filepath.Glob(dayFile + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("期望隔离出 1 个 .corrupt 备份，实际 %d 个", len(matches))
	}

	// 索引不能比数据活得久：重发的 3 条必须能重新入库。
	if res.Ingested != 3 {
		t.Fatalf("重发后 ingested=%d dedupedById=%d，期望 3 条全部重新入库",
			res.Ingested, res.DedupedByID)
	}
	entries, err := readStoredFile(dayFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("当天文件 %d 条，期望 3 条", len(entries))
	}
}

// 落盘失败时不能记索引，否则这批通知重发也进不来。
func TestFailedFlushDoesNotPoisonIndexes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")
	s := newTestStorage(t, dir)

	// 用一个非空目录占住当天文件路径，让原子写的 rename 失败。
	// 顺带预热 dayCache：否则 loadDay 会把这个目录当成损坏文件隔离走，
	// 路径反而腾空了——这里要验证的是落盘真的失败之后的回滚。
	blocker := s.dayPath(dateKey)
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.dayCache[dateKey] = nil

	items := []RawNotification{notifAt("a", now, "1"), notifAt("b", now, "2")}
	res := s.Ingest(items, "probe")
	if res.Ingested != 0 || res.Failed != 2 {
		t.Fatalf("ingested=%d failed=%d，期望 0/2", res.Ingested, res.Failed)
	}

	// 移开障碍后重发：两条都应该进得来，说明失败那批没有在索引里留下占位。
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	delete(s.dayCache, dateKey)
	res = s.Ingest(items, "probe")
	if res.Ingested != 2 {
		t.Fatalf("重发 ingested=%d dedupedById=%d dedupedByContent=%d，期望 2 条入库",
			res.Ingested, res.DedupedByID, res.DedupedByContent)
	}
}

// 同一批里的重复仍然要被挡掉（索引占位不能因为延迟落盘而失效）。
func TestIntraBatchDedupStillApplies(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := newTestStorage(t, dir)

	res := s.Ingest([]RawNotification{
		notifAt("a", now, "1"),
		notifAt("a", now, "1"), // 同 id
		notifAt("", now, "1"),  // 无 id，但内容与第一条一致
	}, "probe")

	if res.Ingested != 1 {
		t.Fatalf("ingested=%d，期望 1", res.Ingested)
	}
	if res.DedupedByID != 1 || res.DedupedByContent != 1 {
		t.Fatalf("dedupedById=%d dedupedByContent=%d，期望 1/1", res.DedupedByID, res.DedupedByContent)
	}

	entries, err := readStoredFile(filepath.Join(dir, now.Format("2006-01-02")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("当天落盘 %d 条，期望 1", len(entries))
	}
}

// 落盘的仍然是合法 JSON 数组，且已有数据会被保留。
func TestIngestAppendsToExistingDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dayFile := filepath.Join(dir, now.Format("2006-01-02")+".json")

	s := newTestStorage(t, dir)
	s.Ingest([]RawNotification{notifAt("a", now, "1")}, "probe")

	s2 := newTestStorage(t, dir)
	s2.Ingest([]RawNotification{notifAt("b", now, "2")}, "probe")

	raw, err := os.ReadFile(dayFile)
	if err != nil {
		t.Fatal(err)
	}
	var entries []StoredNotification
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("落盘内容不是合法 JSON 数组: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("当天 %d 条，期望 2 条（新旧都在）", len(entries))
	}
	if !strings.HasSuffix(strings.TrimSpace(string(raw)), "]") {
		t.Fatalf("落盘内容看起来被截断了")
	}
}

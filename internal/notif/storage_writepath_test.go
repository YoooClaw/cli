package notif

import (
	"bytes"
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

// 一批通知里同一天只应落盘一次，而且写的是 .jsonl 追加格式。
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
		if _, err := os.Stat(dayFilePath(dir, key)); err != nil {
			t.Fatalf("%s 应落盘为 .jsonl: %v", key, err)
		}
		entries := readDateFile(dir, key)
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

// 旧格式 .json 数组在首次落盘该天时迁移为 .jsonl：旧数据在前、新数据追加在后，
// 旧文件删除，且对旧数据的去重依旧生效。
func TestLegacyDayFileMigratedOnFirstFlush(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")
	legacy := []StoredNotification{
		{ClientLabel: "probe", AppName: "com.tencent.xin", AppDisplayName: "com.tencent.xin",
			Title: "老板", Content: "旧-1", Timestamp: now.Format(time.RFC3339)},
		{ClientLabel: "probe", AppName: "com.tencent.xin", AppDisplayName: "com.tencent.xin",
			Title: "老板", Content: "旧-2", Timestamp: now.Format(time.RFC3339)},
	}
	writeDateFile(t, dir, dateKey, legacy)

	s := newTestStorage(t, dir)
	res := s.Ingest([]RawNotification{
		notifAt("dup", now, "旧-1"), // 与旧数据内容一致 -> 内容去重
		notifAt("new", now, "新-1"),
	}, "probe")
	if res.DedupedByContent != 1 || res.Ingested != 1 {
		t.Fatalf("dedupedByContent=%d ingested=%d，期望 1/1", res.DedupedByContent, res.Ingested)
	}

	if _, err := os.Stat(legacyDayFilePath(dir, dateKey)); !os.IsNotExist(err) {
		t.Fatalf("迁移后旧 .json 应被删除, err=%v", err)
	}
	entries := readDateFile(dir, dateKey)
	if len(entries) != 3 {
		t.Fatalf("迁移后 %d 条，期望 3（旧 2 + 新 1）", len(entries))
	}
	// 迁移必须保持顺序（sync checkpoint 按下标消费）。
	if entries[0].Content != "旧-1" || entries[1].Content != "旧-2" || entries[2].Content != "新-1" {
		t.Fatalf("迁移后顺序不对: %q %q %q", entries[0].Content, entries[1].Content, entries[2].Content)
	}
}

// .jsonl 与过期的旧 .json 并存时（迁移在删除前 crash 的产物）：读以 .jsonl 为准、
// 日期 key 不重复、下次落盘顺手清掉过期副本。
func TestStaleLegacyCopyIsIgnoredAndRemoved(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")

	s := newTestStorage(t, dir)
	s.Ingest([]RawNotification{notifAt("a", now, "1")}, "probe")
	// 伪造迁移残留：内容已包含在 .jsonl 里的过期旧文件。
	writeDateFile(t, dir, dateKey, readDateFile(dir, dateKey))

	if keys := listDateKeys(dir); len(keys) != 1 || keys[0] != dateKey {
		t.Fatalf("日期 key 应去重: %v", keys)
	}
	if entries := readDateFile(dir, dateKey); len(entries) != 1 {
		t.Fatalf("读取应以 .jsonl 为准，got %d 条", len(entries))
	}

	s2 := newTestStorage(t, dir)
	res := s2.Ingest([]RawNotification{notifAt("b", now, "2")}, "probe")
	if res.Ingested != 1 {
		t.Fatalf("ingested=%d，期望 1: %+v", res.Ingested, res)
	}
	if _, err := os.Stat(legacyDayFilePath(dir, dateKey)); !os.IsNotExist(err) {
		t.Fatalf("过期旧文件应被清理, err=%v", err)
	}
	if entries := readDateFile(dir, dateKey); len(entries) != 2 {
		t.Fatalf("当天 %d 条，期望 2", len(entries))
	}
}

// 损坏的旧格式 .json（老版本非原子写被中断的产物）不能被当成空的一天静默丢弃，
// 必须隔离备份；索引不能比数据活得久，重发要能重新入库。
func TestCorruptLegacyDayFileIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")
	legacyFile := legacyDayFilePath(dir, dateKey)

	s := newTestStorage(t, dir)
	original := []RawNotification{
		notifAt("a", now, "1"), notifAt("b", now, "2"), notifAt("c", now, "3"),
	}
	s.Ingest(original, "probe")

	// 把已落盘的 .jsonl 内容改造成半截旧格式数组，模拟旧版本升级上来的损坏文件。
	entries := readDateFile(dir, dateKey)
	arr, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, arr[:len(arr)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dayFilePath(dir, dateKey)); err != nil {
		t.Fatal(err)
	}

	// 新进程：缓存是空的，重新加载当天。
	s2 := newTestStorage(t, dir)
	res := s2.Ingest(original, "probe")

	// 损坏的字节必须被留下来，而不是被丢弃。
	matches, _ := filepath.Glob(legacyFile + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("期望隔离出 1 个 .corrupt 备份，实际 %d 个", len(matches))
	}

	// 索引不能比数据活得久：重发的 3 条必须能重新入库。
	if res.Ingested != 3 {
		t.Fatalf("重发后 ingested=%d dedupedById=%d，期望 3 条全部重新入库",
			res.Ingested, res.DedupedByID)
	}
	if entries := readDateFile(dir, dateKey); len(entries) != 3 {
		t.Fatalf("当天文件 %d 条，期望 3 条", len(entries))
	}
}

// torn write 留下的半行只损失那一条：读取时跳过，后续追加先补换行把半行了结，
// 丢失的那条重发时能重新入库（其余条靠内容去重挡掉）。
func TestTornLastLineIsSkippedAndRepaired(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")

	full, err := encodeJSONLines([]StoredNotification{
		{ClientLabel: "probe", AppName: "x", Title: "t", Content: "1", Timestamp: now.Format(time.RFC3339)},
		{ClientLabel: "probe", AppName: "x", Title: "t", Content: "2", Timestamp: now.Format(time.RFC3339)},
		{ClientLabel: "probe", AppName: "x", Title: "t", Content: "3", Timestamp: now.Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatal(err)
	}
	torn := full[:len(full)-10] // 掐掉最后一行的尾巴（含换行）
	if err := os.WriteFile(dayFilePath(dir, dateKey), torn, 0o644); err != nil {
		t.Fatal(err)
	}

	if entries := readDateFile(dir, dateKey); len(entries) != 2 {
		t.Fatalf("半行应被跳过，读到 %d 条，期望 2", len(entries))
	}

	s := newTestStorage(t, dir)
	res := s.Ingest([]RawNotification{
		{ID: "n3", App: "x", Title: "t", Body: "3", Timestamp: now.Format(time.RFC3339)},
	}, "probe")
	if res.Ingested != 1 {
		t.Fatalf("丢失的那条应能重新入库: %+v", res)
	}

	raw, err := os.ReadFile(dayFilePath(dir, dateKey))
	if err != nil {
		t.Fatal(err)
	}
	// 半行必须被换行了结成独立坏行，新数据不能接在它后面变成垃圾。
	if bytes.Contains(raw, []byte(`}{`)) {
		t.Fatalf("新行接在半行后面了: %q", raw)
	}
	entries := readDateFile(dir, dateKey)
	if len(entries) != 3 {
		t.Fatalf("修复追加后 %d 条，期望 3（半行仍是坏行）", len(entries))
	}
	if entries[2].Content != "3" {
		t.Fatalf("追加的应是丢失的那条: %+v", entries[2])
	}
}

// 落盘失败时不能记索引，否则这批通知重发也进不来。
func TestFailedFlushDoesNotPoisonIndexes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")
	s := newTestStorage(t, dir)

	// 用一个目录占住当天 .jsonl 路径，让追加写 open 失败。
	blocker := s.dayPath(dateKey)
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}

	items := []RawNotification{notifAt("a", now, "1"), notifAt("b", now, "2")}
	res := s.Ingest(items, "probe")
	if res.Ingested != 0 || res.Failed != 2 {
		t.Fatalf("ingested=%d failed=%d，期望 0/2", res.Ingested, res.Failed)
	}

	// 移开障碍后重发：两条都应该进得来，说明失败那批没有在索引里留下占位。
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
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

	entries := readDateFile(dir, now.Format("2006-01-02"))
	if len(entries) != 1 {
		t.Fatalf("当天落盘 %d 条，期望 1", len(entries))
	}
}

// 落盘的是逐行合法 JSON 的 .jsonl，跨进程追加时已有数据会被保留。
func TestIngestAppendsToExistingDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateKey := now.Format("2006-01-02")

	s := newTestStorage(t, dir)
	s.Ingest([]RawNotification{notifAt("a", now, "1")}, "probe")

	s2 := newTestStorage(t, dir)
	s2.Ingest([]RawNotification{notifAt("b", now, "2")}, "probe")

	raw, err := os.ReadFile(dayFilePath(dir, dateKey))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("落盘内容应以换行结尾")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("当天 %d 行，期望 2 行（新旧都在）", len(lines))
	}
	for i, line := range lines {
		var entry StoredNotification
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", i, err)
		}
	}
}

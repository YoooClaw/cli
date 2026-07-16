package notif

import (
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/testutil"
)

func newStorage(t *testing.T) *Storage {
	t.Helper()
	s := NewStorage(t.TempDir(), PluginConfig{}, testutil.Logger{T: t})
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIngestBasic(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	res := s.Ingest([]RawNotification{
		{ID: "1", App: "wechat", Title: "t1", Body: "b1", Timestamp: "2026-06-07T10:00:00+08:00"},
		{ID: "2", App: "feishu", Title: "t2", Body: "b2", Timestamp: "2026-06-07T11:00:00+08:00"},
	}, "phone-a")
	if res.Received != 2 || res.Ingested != 2 {
		t.Fatalf("ingest result: %+v", res)
	}
	if len(res.Inserted) != 2 {
		t.Errorf("inserted should be 2: %+v", res.Inserted)
	}
}

func TestIngestEvictsColdDayCache(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	old := RawNotification{ID: "old", App: "wechat", Title: "t", Body: "b", Timestamp: "2026-06-07T10:00:00+08:00"}
	s.Ingest([]RawNotification{old}, "phone-a")
	// 本批触到的日期允许留在缓存；下一批（不含该日期）后必须被逐出，
	// 否则长期运行会把历史每一天的全量通知都常驻内存。
	s.Ingest([]RawNotification{{ID: "now", App: "wechat", Title: "t2", Body: "b2", Timestamp: time.Now().Format(time.RFC3339)}}, "phone-a")
	s.mu.Lock()
	_, cached := s.dayCache["2026-06-07"]
	s.mu.Unlock()
	if cached {
		t.Fatal("cold day should be evicted from dayCache")
	}
	// 逐出后去重依旧生效（从磁盘/索引文件重建）。
	res := s.Ingest([]RawNotification{old}, "phone-a")
	if res.DedupedByID != 1 || res.Ingested != 0 {
		t.Fatalf("dedup must survive cache eviction: %+v", res)
	}
}

func TestIngestDedupByID(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	item := RawNotification{ID: "dup", App: "wechat", Title: "t", Body: "b", Timestamp: "2026-06-07T10:00:00+08:00"}
	s.Ingest([]RawNotification{item}, "phone-a")
	res := s.Ingest([]RawNotification{item}, "phone-a")
	if res.DedupedByID != 1 || res.Ingested != 0 {
		t.Errorf("expected dedupe by id: %+v", res)
	}
}

func TestIngestDedupByContent(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	// 不同 ID，相同内容 -> 内容去重
	a := RawNotification{ID: "a", App: "wechat", Title: "same", Body: "same body", Timestamp: "2026-06-07T10:00:00+08:00"}
	b := RawNotification{ID: "b", App: "wechat", Title: "same", Body: "same body", Timestamp: "2026-06-07T10:00:00+08:00"}
	s.Ingest([]RawNotification{a}, "phone-a")
	res := s.Ingest([]RawNotification{b}, "phone-a")
	if res.DedupedByContent != 1 {
		t.Errorf("expected dedupe by content: %+v", res)
	}
}

func TestIngestInvalidTimestamp(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	res := s.Ingest([]RawNotification{
		{ID: "x", App: "wechat", Title: "t", Body: "b", Timestamp: "not-a-time"},
	}, "phone-a")
	if res.Invalid != 1 || res.Ingested != 0 {
		t.Errorf("expected invalid: %+v", res)
	}
}

func TestIngestDefaultLabelAndUnknownApp(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	res := s.Ingest([]RawNotification{
		{ID: "1", Title: "t", Body: "b", Timestamp: "2026-06-07T10:00:00+08:00"},
	}, "")
	if res.Ingested != 1 {
		t.Fatalf("ingest: %+v", res)
	}
	got := res.Inserted[0]
	if got.ClientLabel != "default" || got.AppName != "Unknown" || got.AppDisplayName != "Unknown" {
		t.Errorf("default label / unknown app wrong: %+v", got)
	}
}

func TestIngestFallbackContent(t *testing.T) {
	t.Parallel()
	s := newStorage(t)
	res := s.Ingest([]RawNotification{
		{ID: "1", App: "x", Title: "t", Timestamp: "2026-06-07T10:00:00+08:00", Category: "msg", Metadata: map[string]any{"k": "v"}},
	}, "p")
	c := res.Inserted[0].Content
	if c == "" || c == "-" {
		t.Errorf("fallback content should include category/metadata, got %q", c)
	}
}

func TestIngestPrune(t *testing.T) {
	t.Parallel()
	days := 1
	s := NewStorage(t.TempDir(), PluginConfig{RetentionDays: &days}, testutil.Logger{T: t})
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	// 一条很旧的通知，prune 后该日期文件应被删除
	s.Ingest([]RawNotification{
		{ID: "old", App: "x", Title: "t", Body: "b", Timestamp: "2020-01-01T10:00:00+08:00"},
	}, "p")
	// 再 ingest 一条今天的，触发 prune
	s.Ingest([]RawNotification{
		{ID: "new", App: "x", Title: "t", Body: "b2", Timestamp: timeNowISO()},
	}, "p")
	q := Query(s.dir, QueryOptions{Limit: 10})
	for _, n := range q {
		if n.Content == "b" {
			t.Error("old notification should have been pruned")
		}
	}
}

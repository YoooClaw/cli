package yclib_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/yclib"
)

// seedNotifications 在显式 RootDir 下按 profile 写入一个日期文件，返回该 root。
func seedNotifications(t *testing.T, profile string, items []notif.StoredNotification, dateKey string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "profiles", profile, "notifications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, dateKey+".json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

func TestNotificationsSearch_EndToEnd(t *testing.T) {
	items := []notif.StoredNotification{
		{AppName: "com.feishu", AppDisplayName: "飞书", Title: "周会", Content: "下午三点开会", Timestamp: "2026-06-22T15:00:00+08:00"},
		{AppName: "com.wechat", AppDisplayName: "微信", Title: "午饭", Content: "一起吃饭吗", Timestamp: "2026-06-22T11:30:00+08:00"},
	}
	root := seedNotifications(t, "default", items, "2026-06-22")

	c, err := yclib.New(yclib.Config{RootDir: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Profile() != "default" {
		t.Fatalf("profile = %q, want default", c.Profile())
	}

	// 全量查询：两条都命中，时间倒序（周会 15:00 在前）。
	res, err := c.Notifications().Search(context.Background(), notif.RawQueryOpts{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Notifications) != 2 {
		t.Fatalf("got %d notifications, want 2", len(res.Notifications))
	}
	if res.Notifications[0].Title != "周会" {
		t.Errorf("first title = %q, want 周会 (desc by time)", res.Notifications[0].Title)
	}
	if res.Stats.Matched != 2 {
		t.Errorf("stats.Matched = %d, want 2", res.Stats.Matched)
	}

	// 关键字过滤：只命中含“开会”的一条。
	res, err = c.Notifications().Search(context.Background(), notif.RawQueryOpts{Keyword: "开会"})
	if err != nil {
		t.Fatalf("Search keyword: %v", err)
	}
	if len(res.Notifications) != 1 || res.Notifications[0].Title != "周会" {
		t.Fatalf("keyword filter got %+v, want single 周会", res.Notifications)
	}
}

func TestNotificationsSearch_EmptyIsNotError(t *testing.T) {
	// RootDir 指向空目录：notifications 目录不存在 → 正常返回空，非错误。
	c, err := yclib.New(yclib.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := c.Notifications().Search(context.Background(), notif.RawQueryOpts{})
	if err != nil {
		t.Fatalf("Search on empty: %v", err)
	}
	if len(res.Notifications) != 0 {
		t.Fatalf("got %d, want 0", len(res.Notifications))
	}
}

func TestNotificationsSearch_InvalidArgIsTypedError(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	_, err := c.Notifications().Search(context.Background(), notif.RawQueryOpts{ConversationType: "bogus"})
	if err == nil {
		t.Fatal("want error for invalid conversation-type, got nil")
	}
	if code := yclib.ErrorCode(err); code != yclib.CodeInvalidArgument {
		t.Fatalf("ErrorCode = %q, want %q", code, yclib.CodeInvalidArgument)
	}
}

func TestNotificationsSearch_ContextCancelled(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Notifications().Search(ctx, yclib.RawQueryOpts{}); err == nil {
		t.Fatal("want context.Canceled, got nil")
	}
}

func TestNotificationsSummary_EndToEnd(t *testing.T) {
	items := []notif.StoredNotification{
		{AppName: "com.feishu", AppDisplayName: "飞书", Title: "周会", SenderName: "Alice", Timestamp: "2026-06-22T15:00:00+08:00"},
		{AppName: "com.feishu", AppDisplayName: "飞书", Title: "日报", SenderName: "Alice", Timestamp: "2026-06-22T18:00:00+08:00"},
		{AppName: "com.wechat", AppDisplayName: "微信", Title: "午饭", SenderName: "Bob", Timestamp: "2026-06-22T11:30:00+08:00"},
	}
	root := seedNotifications(t, "default", items, "2026-06-22")
	c, _ := yclib.New(yclib.Config{RootDir: root})

	res, err := c.Notifications().Summary(context.Background(), yclib.SummaryOpts{})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if res.Total != 3 {
		t.Fatalf("Total = %d, want 3", res.Total)
	}
	// 飞书 2 条排第一，微信 1 条第二。
	if len(res.TopApps) != 2 || res.TopApps[0].Key != "飞书" || res.TopApps[0].Count != 2 {
		t.Fatalf("TopApps = %+v, want 飞书=2 first", res.TopApps)
	}
	if len(res.Sample) != 3 {
		t.Errorf("Sample len = %d, want 3", len(res.Sample))
	}
}

func TestNotificationsStats_DimFiltering(t *testing.T) {
	items := []notif.StoredNotification{
		{AppName: "com.feishu", AppDisplayName: "飞书", Title: "周会", Timestamp: "2026-06-22T15:00:00+08:00"},
		{AppName: "com.wechat", AppDisplayName: "微信", Title: "午饭", Timestamp: "2026-06-22T11:30:00+08:00"},
	}
	root := seedNotifications(t, "default", items, "2026-06-22")
	c, _ := yclib.New(yclib.Config{RootDir: root})
	ctx := context.Background()

	// dim=app：只填充 App 维度，其余为 nil。
	res, err := c.Notifications().Stats(ctx, yclib.StatsOpts{From: "2026-06-22", To: "2026-06-22", Dim: "app"})
	if err != nil {
		t.Fatalf("Stats app: %v", err)
	}
	if res.Total != 2 || len(res.App) != 2 {
		t.Fatalf("App dim = %+v (total %d), want 2 apps", res.App, res.Total)
	}
	if res.Date != nil || res.Hour != nil {
		t.Errorf("non-requested dims should be nil, got Date=%v Hour=%v", res.Date, res.Hour)
	}

	// dim=all：五个维度都填充。
	res, err = c.Notifications().Stats(ctx, yclib.StatsOpts{From: "2026-06-22", To: "2026-06-22", Dim: "all"})
	if err != nil {
		t.Fatalf("Stats all: %v", err)
	}
	if res.Date == nil || res.App == nil || res.Sender == nil || res.Hour == nil || res.Client == nil {
		t.Fatalf("dim=all should fill every dimension, got %+v", res)
	}

	// 非法 dim → typed invalid-arg。
	if _, err := c.Notifications().Stats(ctx, yclib.StatsOpts{Dim: "bogus"}); yclib.ErrorCode(err) != yclib.CodeInvalidArgument {
		t.Fatalf("bogus dim ErrorCode = %q, want %q", yclib.ErrorCode(err), yclib.CodeInvalidArgument)
	}
}

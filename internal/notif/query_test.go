package notif

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestParseTime(t *testing.T) {
	t.Parallel()
	for _, v := range []string{
		"2026-06-07T10:00:00+08:00",
		"2026-06-07T10:00:00Z",
		"2026-06-07T10:00Z",
		"2026-06-07T10:00:00.123Z",
	} {
		if _, ok := ParseTime(v); !ok {
			t.Errorf("ParseTime(%q) should succeed", v)
		}
	}
	if _, ok := ParseTime("not-a-time"); ok {
		t.Error("invalid time should fail")
	}
}

func TestBuildQueryOptions(t *testing.T) {
	t.Parallel()
	opts, err := BuildQueryOptions(RawQueryOpts{
		From: "2026-06-01T00:00:00+08:00", To: "2026-06-30T00:00:00+08:00",
		App: "wechat", Limit: "50", ConversationType: "group",
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Limit != 50 || opts.FromDateKey != "2026-06-01" || opts.ToDateKey != "2026-06-30" {
		t.Errorf("opts = %+v", opts)
	}
}

func TestBuildQueryOptionsErrors(t *testing.T) {
	t.Parallel()
	cases := []RawQueryOpts{
		{ConversationType: "channel"},                                            // 非法 type
		{From: "bad-date"},                                                       // 非法 from
		{To: "bad-date"},                                                         // 非法 to
		{From: "2026-06-30T00:00:00Z", To: "2026-06-01T00:00:00Z"},               // from > to
		{Limit: "0"},                                                             // limit <= 0
		{Limit: "abc"},                                                           // limit 非数字
	}
	for i, c := range cases {
		if _, err := BuildQueryOptions(c, 20); err == nil {
			t.Errorf("case %d (%+v) should error", i, c)
		} else if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeInvalidArgument {
			t.Errorf("case %d should be INVALID_ARGUMENT, got %v", i, err)
		}
	}
}

func TestBuildQueryOptionsDefaultLimit(t *testing.T) {
	t.Parallel()
	opts, err := BuildQueryOptions(RawQueryOpts{}, 20)
	if err != nil || opts.Limit != 20 {
		t.Errorf("default limit should be 20, got %d err=%v", opts.Limit, err)
	}
}

func TestMatchesAppFilter(t *testing.T) {
	t.Parallel()
	wechat := StoredNotification{AppName: "com.tencent.mm", AppDisplayName: "微信"}
	if !MatchesAppFilter(wechat, "wechat") {
		t.Error("alias 'wechat' should match com.tencent.mm")
	}
	if !MatchesAppFilter(wechat, "微信") {
		t.Error("display name should match")
	}
	if MatchesAppFilter(wechat, "feishu") {
		t.Error("feishu should not match wechat")
	}
	if !MatchesAppFilter(wechat, "") {
		t.Error("empty filter matches all")
	}
	if MatchesAppFilter(StoredNotification{AppName: "x"}, "totallyunknown") {
		t.Error("unknown app + unknown filter should not match")
	}
}

func TestMatchesQuery(t *testing.T) {
	t.Parallel()
	item := StoredNotification{
		ClientLabel: "phone-a", AppName: "feishu", Title: "群名", Content: "hello world",
		SenderName: "Alice", ConversationType: "group", ConversationName: "Team",
		Timestamp: "2026-06-07T10:00:00+08:00",
	}
	base := QueryOptions{Limit: 10}
	if !MatchesQuery(item, base) {
		t.Error("empty query should match")
	}
	if MatchesQuery(item, QueryOptions{Client: "phone-b"}) {
		t.Error("client mismatch should fail")
	}
	if !MatchesQuery(item, QueryOptions{Client: "all"}) {
		t.Error("client=all should match")
	}
	if MatchesQuery(item, QueryOptions{ConversationType: "private"}) {
		t.Error("conversation type mismatch should fail")
	}
	if !MatchesQuery(item, QueryOptions{Sender: "Alice"}) {
		t.Error("sender substring should match")
	}
	if !MatchesQuery(item, QueryOptions{Keyword: "world"}) {
		t.Error("keyword should match content")
	}
	if MatchesQuery(item, QueryOptions{Keyword: "absent"}) {
		t.Error("absent keyword should fail")
	}
	// legacy label
	legacy := item
	legacy.ClientLabel = ""
	if !MatchesQuery(legacy, QueryOptions{Client: "legacy"}) {
		t.Error("empty label should match client=legacy")
	}
	// bad timestamp
	bad := item
	bad.Timestamp = "garbage"
	if MatchesQuery(bad, base) {
		t.Error("unparsable timestamp should fail match")
	}
}

func TestMatchesQueryTimeRange(t *testing.T) {
	t.Parallel()
	item := StoredNotification{AppName: "x", Timestamp: "2026-06-07T10:00:00Z"}
	from := time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)
	if !MatchesQuery(item, QueryOptions{FromTs: &from, ToTs: &to}) {
		t.Error("within range should match")
	}
	early := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	if MatchesQuery(item, QueryOptions{FromTs: &early}) {
		t.Error("before from should fail")
	}
}

func TestQueryEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Query on missing dir
	if got := Query(filepath.Join(dir, "nope"), QueryOptions{Limit: 10}); got != nil {
		t.Error("missing dir should return nil")
	}
	writeDateFile(t, dir, "2026-06-07", []StoredNotification{
		{AppName: "wechat", Title: "a", Content: "first", Timestamp: "2026-06-07T08:00:00Z"},
		{AppName: "feishu", Title: "b", Content: "second", Timestamp: "2026-06-07T09:00:00Z"},
	})
	got := Query(dir, QueryOptions{Limit: 10})
	if len(got) != 2 || got[0].Content != "second" {
		t.Fatalf("expected newest-first 2 results: %+v", got)
	}
	// app filter
	wx := Query(dir, QueryOptions{App: "wechat", Limit: 10})
	if len(wx) != 1 || wx[0].AppName != "wechat" {
		t.Errorf("app filter failed: %+v", wx)
	}
	// limit
	if lim := Query(dir, QueryOptions{Limit: 1}); len(lim) != 1 {
		t.Errorf("limit failed: %+v", lim)
	}
}

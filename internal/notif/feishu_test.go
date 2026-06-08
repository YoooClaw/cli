package notif

import "testing"

func TestIsFeishuApp(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"feishu", "Lark", "com.ss.android.lark", "飞书", "my-feishu-clone"} {
		if !isFeishuApp(ok) {
			t.Errorf("%q should be feishu", ok)
		}
	}
	for _, no := range []string{"", "wechat", "com.tencent.mm"} {
		if isFeishuApp(no) {
			t.Errorf("%q should not be feishu", no)
		}
	}
}

func TestExtractColonSender(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Alice: hi":   "Alice",
		"Bob：你好":      "Bob",
		"no colon":    "",
		"":            "",
		":leading":    "",
	}
	for in, want := range tests {
		if got := extractColonSender(in); got != want {
			t.Errorf("extractColonSender(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeFeishuNonFeishu(t *testing.T) {
	t.Parallel()
	if _, ok := normalizeFeishuFields(RawNotification{App: "wechat", Title: "x", Body: "y"}); ok {
		t.Error("non-feishu should return false")
	}
}

func TestNormalizeFeishuGroupViaMetadata(t *testing.T) {
	t.Parallel()
	n := RawNotification{
		App: "feishu", Title: "Alice", Body: "hello team",
		Metadata: map[string]any{"subtitle": "研发群"},
	}
	got, ok := normalizeFeishuFields(n)
	if !ok {
		t.Fatal("expected feishu normalization")
	}
	if got.Structured.ConversationType != "group" || got.Structured.ConversationName != "研发群" {
		t.Errorf("structured wrong: %+v", got.Structured)
	}
	if got.Title != "研发群" {
		t.Errorf("group title should be conversation name, got %q", got.Title)
	}
	if got.Content != "Alice: hello team" {
		t.Errorf("group content should prefix sender, got %q", got.Content)
	}
}

func TestNormalizeFeishuPrivateViaMetadata(t *testing.T) {
	t.Parallel()
	n := RawNotification{
		App: "lark", Title: "Bob", Body: "Bob: 私聊内容",
		Metadata: map[string]any{"subtitle": ""},
	}
	got, ok := normalizeFeishuFields(n)
	if !ok {
		t.Fatal("expected normalization")
	}
	if got.Structured.ConversationType != "private" || got.Structured.SenderName != "Bob" {
		t.Errorf("private structured wrong: %+v", got.Structured)
	}
	if got.Content != "私聊内容" {
		t.Errorf("private content should strip sender prefix, got %q", got.Content)
	}
}

func TestNormalizeFeishuColonFallback(t *testing.T) {
	t.Parallel()
	// 无 metadata.subtitle，但 Title 是 feishu 标签且 body 含冒号发送人
	n := RawNotification{App: "feishu", Title: "飞书", Body: "Carol: 在吗"}
	got, ok := normalizeFeishuFields(n)
	if !ok {
		t.Fatal("expected colon-fallback normalization")
	}
	if got.Structured.SenderName != "Carol" || got.Structured.ConversationType != "private" {
		t.Errorf("colon fallback wrong: %+v", got.Structured)
	}
}

func TestFindMetadataTextByKeyNested(t *testing.T) {
	t.Parallel()
	meta := map[string]any{
		"outer": []any{
			map[string]any{"inner": map[string]any{"Subtitle": "群名X"}},
		},
	}
	found, text := findMetadataTextByKey(meta, "subtitle", 0)
	if !found || text != "群名X" {
		t.Errorf("nested lookup failed: found=%v text=%q", found, text)
	}
	if f, _ := findMetadataTextByKey(meta, "absent", 0); f {
		t.Error("absent key should not be found")
	}
}

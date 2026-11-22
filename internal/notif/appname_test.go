package notif

import (
	"os"
	"path/filepath"
	"testing"
)

// useAppNameMapFile 把快照覆盖为指定内容；必须与 t.Parallel 互斥。
func useAppNameMapFile(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app-name-map.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(appNameMapFileEnv, path)
	ResetAppNameMapForTest()
	t.Cleanup(ResetAppNameMapForTest)
}

func TestBundledAppNameMap(t *testing.T) {
	t.Parallel()

	if size := AppNameMapSize(); size < 1000 {
		t.Fatalf("bundled app name map has %d entries, want >= 1000", size)
	}
	for pkg, want := range map[string]string{
		"com.tencent.xin":           "微信",
		"com.tencent.mm":            "微信",
		"com.ss.android.lark":       "飞书",
		"com.alibaba.android.rimet": "钉钉",
	} {
		if got := ResolveAppDisplayName(pkg); got != want {
			t.Errorf("ResolveAppDisplayName(%q) = %q, want %q", pkg, got, want)
		}
	}
}

func TestResolveAppDisplayNameFallback(t *testing.T) {
	t.Parallel()

	if got := ResolveAppDisplayName("com.example.unknown"); got != "com.example.unknown" {
		t.Errorf("unknown package = %q, want original package name", got)
	}
	if got := ResolveAppDisplayName(""); got != "" {
		t.Errorf("empty package = %q, want empty", got)
	}
}

func TestLookupAppDisplayNameLanguageFallback(t *testing.T) {
	useAppNameMapFile(t, `{
		"com.both": {"zh": "中文名", "en": "English"},
		"com.en.only": {"en": "English Only"},
		"com.zh.only": {"zh": "仅中文"},
		"com.empty": {}
	}`)

	if got, ok := LookupAppDisplayName("com.both"); !ok || got != "中文名" {
		t.Errorf("com.both = %q, %v; want 中文名, true", got, ok)
	}
	if got, ok := LookupAppDisplayName("com.en.only"); !ok || got != "English Only" {
		t.Errorf("com.en.only = %q, %v; want English Only, true", got, ok)
	}
	if got, ok := LookupAppDisplayName("com.zh.only"); !ok || got != "仅中文" {
		t.Errorf("com.zh.only = %q, %v; want 仅中文, true", got, ok)
	}
	if got, ok := LookupAppDisplayName("com.empty"); ok {
		t.Errorf("com.empty = %q, true; want no hit", got)
	}
}

func TestLegacyStringAppNameMap(t *testing.T) {
	useAppNameMapFile(t, `{"com.legacy": "老格式"}`)

	if got := ResolveAppDisplayName("com.legacy"); got != "老格式" {
		t.Errorf("legacy string entry = %q, want 老格式", got)
	}
}

func TestMissingAppNameMapFallsBack(t *testing.T) {
	t.Setenv(appNameMapFileEnv, filepath.Join(t.TempDir(), "missing.json"))
	ResetAppNameMapForTest()
	t.Cleanup(ResetAppNameMapForTest)

	if got := AppNameMapSize(); got != 0 {
		t.Errorf("missing snapshot size = %d, want 0", got)
	}
	if got := ResolveAppDisplayName("com.tencent.xin"); got != "com.tencent.xin" {
		t.Errorf("missing snapshot = %q, want package name", got)
	}
}

func TestCorruptAppNameMapFallsBack(t *testing.T) {
	useAppNameMapFile(t, "{not json")

	if got := AppNameMapSize(); got != 0 {
		t.Errorf("corrupt snapshot size = %d, want 0", got)
	}
	if got := ResolveAppDisplayName("com.tencent.xin"); got != "com.tencent.xin" {
		t.Errorf("corrupt snapshot = %q, want package name", got)
	}
}

func TestIngestWritesMappedAppDisplayName(t *testing.T) {
	t.Parallel()

	s := newStorage(t)
	res := s.Ingest([]RawNotification{{
		ID: "mapped", App: "com.tencent.xin", Title: "t", Body: "b",
		Timestamp: "2026-06-07T10:00:00+08:00",
	}}, "phone-a")
	if res.Ingested != 1 || len(res.Inserted) != 1 {
		t.Fatalf("ingest = %+v, want one inserted", res)
	}
	got := res.Inserted[0]
	if got.AppName != "com.tencent.xin" {
		t.Errorf("AppName = %q, want package name", got.AppName)
	}
	if got.AppDisplayName != "微信" {
		t.Errorf("AppDisplayName = %q, want 微信", got.AppDisplayName)
	}
}

func TestIngestUnknownAppKeepsPackageName(t *testing.T) {
	t.Parallel()

	s := newStorage(t)
	res := s.Ingest([]RawNotification{{
		ID: "unknown", App: "com.example.unknown", Title: "t", Body: "b",
		Timestamp: "2026-06-07T10:00:00+08:00",
	}}, "phone-a")
	if res.Ingested != 1 || len(res.Inserted) != 1 {
		t.Fatalf("ingest = %+v, want one inserted", res)
	}
	got := res.Inserted[0]
	if got.AppName != "com.example.unknown" || got.AppDisplayName != "com.example.unknown" {
		t.Errorf("unknown app = %+v, want both fields to be package name", got)
	}
}

func TestIngestDedupIgnoresDisplayName(t *testing.T) {
	t.Parallel()

	s := newStorage(t)
	first := RawNotification{
		ID: "a", App: "com.tencent.mm", Title: "same", Body: "same body",
		Timestamp: "2026-06-07T10:00:00+08:00",
	}
	if res := s.Ingest([]RawNotification{first}, "phone-a"); res.Ingested != 1 {
		t.Fatalf("first ingest = %+v, want one inserted", res)
	}
	second := first
	second.ID = "b"
	res := s.Ingest([]RawNotification{second}, "phone-a")
	if res.DedupedByContent != 1 || res.Ingested != 0 {
		t.Errorf("second ingest = %+v, want deduplicated by content", res)
	}
}

// 本次范围刻意不改历史数据；读取旧行时不允许用当前快照覆盖落库字段。
func TestHistoricalAppDisplayNameNotRewrittenOnRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDateFile(t, dir, "2026-06-01", []StoredNotification{{
		AppName: "com.tencent.xin", AppDisplayName: "com.tencent.xin",
		Title: "old", Content: "old", Timestamp: "2026-06-01T10:00:00+08:00",
	}})

	items := Query(dir, QueryOptions{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("Query returned %d items, want 1", len(items))
	}
	if items[0].AppDisplayName != "com.tencent.xin" {
		t.Errorf("historical display name = %q, want unchanged package name", items[0].AppDisplayName)
	}
}

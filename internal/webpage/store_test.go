package webpage

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testLogger struct{ warns []string }

func (l *testLogger) Info(string)     {}
func (l *testLogger) Warn(msg string) { l.warns = append(l.warns, msg) }

func samplePayload() Payload {
	return Payload{
		URL:           "https://example.com/a?utm_source=x",
		CanonicalURL:  "https://example.com/a",
		Title:         "Array.prototype.map()",
		SiteName:      "MDN Web Docs",
		Byline:        "MDN contributors",
		Language:      "en",
		PublishedTime: "2026-03-11",
		CapturedAt:    "2026-07-27T15:04:22+08:00",
		Markdown:      "正文一段。\n",
		ContentHash:   "9f86d081884c7d65",
	}
}

func gzipBase64(t *testing.T, payload string) string {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestIngestWritesMarkdownAndIndex(t *testing.T) {
	dir := t.TempDir()
	logger := &testLogger{}

	result, err := Ingest(dir, samplePayload(), "webext", logger)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	sum := sha256.Sum256([]byte("https://example.com/a"))
	wantHash := hex.EncodeToString(sum[:])
	if result.URLHash != wantHash {
		t.Errorf("urlHash = %q, 期望 %q", result.URLHash, wantHash)
	}
	if result.Replaced || result.CaptureCount != 1 {
		t.Errorf("首次收藏应当 replaced=false captureCount=1，得到 %+v", result)
	}
	want := "files/" + wantHash[:8] + "-array-prototype-map.md"
	if result.RelativePath != want {
		t.Errorf("relativePath = %q, 期望 %q", result.RelativePath, want)
	}

	raw, err := os.ReadFile(filepath.Join(dir, result.RelativePath))
	if err != nil {
		t.Fatalf("读取落盘文件: %v", err)
	}
	document := string(raw)
	for _, needle := range []string{
		"---\n",
		`url: "https://example.com/a?utm_source=x"`,
		`canonicalUrl: "https://example.com/a"`,
		`title: "Array.prototype.map()"`,
		`siteName: "MDN Web Docs"`,
		`byline: "MDN contributors"`,
		`language: "en"`,
		`publishedTime: "2026-03-11"`,
		`capturedAt: "2026-07-27T15:04:22+08:00"`,
		`contentHash: "9f86d081884c7d65"`,
		`clientLabel: "webext"`,
		"truncated: false\n",
		"lowConfidence: false\n",
		"# Array.prototype.map()",
		"正文一段。",
	} {
		if !strings.Contains(document, needle) {
			t.Errorf("落盘文件缺少 %q:\n%s", needle, document)
		}
	}
	if strings.Contains(document, "archive:") {
		t.Error("没带存档时不应写 archive: 行")
	}

	entries := ReadIndex(dir)
	if len(entries) != 1 {
		t.Fatalf("index 应有 1 条，得到 %d", len(entries))
	}
	entry := entries[0]
	if entry.FirstCapturedAt != entry.CapturedAt || entry.CaptureCount != 1 {
		t.Errorf("首次收藏索引字段不对: %+v", entry)
	}
	if entry.Bytes != len(raw) || entry.ClientLabel != "webext" {
		t.Errorf("索引 bytes/clientLabel 不对: %+v", entry)
	}
}

func TestIngestRecaptureOverwritesAndKeepsFirstCapturedAt(t *testing.T) {
	dir := t.TempDir()
	logger := &testLogger{}
	if _, err := Ingest(dir, samplePayload(), "webext", logger); err != nil {
		t.Fatal(err)
	}

	// 第二次：URL 只差追踪参数（规范化后同一篇），标题也改了。
	second := samplePayload()
	second.CanonicalURL = "https://example.com/a?utm_campaign=later#top"
	second.Title = "Array.prototype.map() — 修订版"
	second.CapturedAt = "2026-07-28T09:00:00+08:00"
	second.Markdown = "# 已有一级标题\n\n新正文。"
	result, err := Ingest(dir, second, "webext", logger)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !result.Replaced || result.CaptureCount != 2 {
		t.Errorf("重收应当 replaced=true captureCount=2，得到 %+v", result)
	}

	entries := ReadIndex(dir)
	if len(entries) != 1 {
		t.Fatalf("重收后 index 仍应只有 1 条，得到 %d", len(entries))
	}
	if entries[0].FirstCapturedAt != "2026-07-27T15:04:22+08:00" {
		t.Errorf("firstCapturedAt 被覆盖了: %q", entries[0].FirstCapturedAt)
	}
	if entries[0].CapturedAt != "2026-07-28T09:00:00+08:00" {
		t.Errorf("capturedAt 没更新: %q", entries[0].CapturedAt)
	}

	// 标题变了 → 文件名变了 → 旧文件必须删掉，一个 URL 只留一个文件。
	files, err := os.ReadDir(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("files/ 应只剩 1 个文件，得到 %v", names)
	}
	if files[0].Name() != filepath.Base(result.RelativePath) {
		t.Errorf("残留的是旧文件: %q", files[0].Name())
	}

	raw, err := os.ReadFile(filepath.Join(dir, result.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	// 正文自带 H1 时不再补一个。
	if strings.Count(string(raw), "\n# ") > 1 {
		t.Errorf("重复注入了 H1:\n%s", raw)
	}
}

func TestIngestStoresArchiveSidecar(t *testing.T) {
	dir := t.TempDir()
	logger := &testLogger{}
	payload := samplePayload()
	payload.Archive = &Archive{Encoding: ArchiveEncoding, Data: gzipBase64(t, "<html><body>存档</body></html>")}

	result, err := Ingest(dir, payload, "webext", logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.ArchivePath == "" || !strings.HasSuffix(result.ArchivePath, ".html") {
		t.Fatalf("archivePath = %q", result.ArchivePath)
	}
	html, err := os.ReadFile(filepath.Join(dir, result.ArchivePath))
	if err != nil {
		t.Fatalf("读取存档: %v", err)
	}
	if string(html) != "<html><body>存档</body></html>" {
		t.Errorf("存档内容被改动: %q", html)
	}
	document, err := os.ReadFile(filepath.Join(dir, result.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document), `archive: "`+result.ArchivePath+`"`) {
		t.Errorf("frontmatter 缺少 archive 行:\n%s", document)
	}
	if entries := ReadIndex(dir); entries[0].ArchivePath != result.ArchivePath || entries[0].ArchiveBytes == 0 {
		t.Errorf("索引存档字段不对: %+v", entries[0])
	}
}

func TestIngestSurvivesBrokenArchive(t *testing.T) {
	// 存档永远不能让一次收藏失败（§4.1）。
	for name, archive := range map[string]Archive{
		"编码不认识":    {Encoding: "zstd+base64", Data: gzipBase64(t, "<p>x</p>")},
		"base64 坏": {Encoding: ArchiveEncoding, Data: "!!!not-base64!!!"},
		"不是 gzip":  {Encoding: ArchiveEncoding, Data: base64.StdEncoding.EncodeToString([]byte("plain"))},
		"不像 HTML":  {Encoding: ArchiveEncoding, Data: gzipBase64(t, "just text")},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			logger := &testLogger{}
			payload := samplePayload()
			payload.Archive = &archive
			result, err := Ingest(dir, payload, "webext", logger)
			if err != nil {
				t.Fatalf("坏存档不应让 ingest 失败: %v", err)
			}
			if result.ArchivePath != "" {
				t.Errorf("坏存档不应回 archivePath: %q", result.ArchivePath)
			}
			if len(logger.warns) == 0 {
				t.Error("坏存档应当留下一条 warn")
			}
			if _, err := os.Stat(filepath.Join(dir, result.RelativePath)); err != nil {
				t.Errorf("Markdown 应当照常落盘: %v", err)
			}
		})
	}
}

func TestIngestRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	logger := &testLogger{}

	cases := map[string]struct {
		mutate func(*Payload)
		status int
	}{
		"缺 canonicalUrl": {func(p *Payload) { p.CanonicalURL, p.URL = "", "" }, 400},
		"非 http":         {func(p *Payload) { p.CanonicalURL = "chrome://settings" }, 400},
		"缺 markdown":     {func(p *Payload) { p.Markdown = "   " }, 400},
		"markdown 超上限":   {func(p *Payload) { p.Markdown = strings.Repeat("a", MaxMarkdownBytes+1) }, 413},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			payload := samplePayload()
			c.mutate(&payload)
			_, err := Ingest(dir, payload, "webext", logger)
			if err == nil {
				t.Fatal("应当报错")
			}
			ingestErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("错误类型不对: %T", err)
			}
			if ingestErr.Status != c.status {
				t.Errorf("status = %d, 期望 %d", ingestErr.Status, c.status)
			}
		})
	}
}

func TestIngestFallsBackToURLWhenCanonicalMissing(t *testing.T) {
	dir := t.TempDir()
	payload := samplePayload()
	payload.CanonicalURL = ""
	result, err := Ingest(dir, payload, "", &testLogger{})
	if err != nil {
		t.Fatal(err)
	}
	// url 上的 utm_source 会被规范化掉，落成与 canonicalUrl 版本同一个 hash。
	sum := sha256.Sum256([]byte("https://example.com/a"))
	if result.URLHash != hex.EncodeToString(sum[:]) {
		t.Errorf("urlHash = %q", result.URLHash)
	}
	if entries := ReadIndex(dir); entries[0].ClientLabel != "default" {
		t.Errorf("clientLabel 应当回落 default: %q", entries[0].ClientLabel)
	}
}

func TestDecodeArchiveRejectsBomb(t *testing.T) {
	data := gzipBase64(t, "<"+strings.Repeat("a", MaxArchiveBytes+16))
	if _, err := DecodeArchive(Archive{Encoding: ArchiveEncoding, Data: data}); err == nil {
		t.Fatal("解压后超限应当被拒")
	}
}

func TestStatusMatchesFullHashAndPrefix(t *testing.T) {
	dir := t.TempDir()
	result, err := Ingest(dir, samplePayload(), "webext", &testLogger{})
	if err != nil {
		t.Fatal(err)
	}
	full := result.URLHash

	saved := Status(dir, []string{full, full[:16], strings.ToUpper(full[:16]), "deadbeef", "abc"}, "")
	if saved[full]["capturedAt"] != "2026-07-27T15:04:22+08:00" {
		t.Errorf("完整 hash 未命中: %+v", saved)
	}
	if _, ok := saved[full[:16]]; !ok {
		t.Error("16 位前缀未命中（扩展本地索引就是这个长度）")
	}
	if _, ok := saved[strings.ToUpper(full[:16])]; !ok {
		t.Error("大写前缀未命中")
	}
	if _, ok := saved["deadbeef"]; ok {
		t.Error("不存在的 hash 不应出现在结果里")
	}
	if _, ok := saved["abc"]; ok {
		t.Error("过短的前缀应当被忽略")
	}
}

func TestProjectFields(t *testing.T) {
	entries := []Entry{{URLHash: "abc", CapturedAt: "t", Title: "标题", Bytes: 12}}

	full := ProjectFields(entries, nil)
	if len(full[0]) < 10 {
		t.Errorf("不给 fields 应返回全字段: %+v", full[0])
	}

	slim := ProjectFields(entries, []string{"hash", "capturedAt"})
	if len(slim[0]) != 2 || slim[0]["urlHash"] != "abc" || slim[0]["capturedAt"] != "t" {
		t.Errorf("fields=hash,capturedAt 投影不对: %+v", slim[0])
	}
}

func TestIngestIgnoresIndexPathsOutsideDir(t *testing.T) {
	// index.json 可能被手工编辑过；旧条目里的越界路径不能被当成删除目标。
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest(dir, samplePayload(), "webext", &testLogger{}); err != nil {
		t.Fatal(err)
	}
	entries := ReadIndex(dir)
	entries[0].RelativePath = "../../" + filepath.Base(filepath.Dir(outside)) + "/victim.md"
	if err := writeIndex(dir, entries); err != nil {
		t.Fatal(err)
	}

	changed := samplePayload()
	changed.Title = "换个标题"
	if _, err := Ingest(dir, changed, "webext", &testLogger{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("目录外的文件被删了: %v", err)
	}
}

// 三端落盘的 .md 与 index.json 条目必须逐字节 / 逐字段一致。
// 这条曾经真的漂过（TS/Python 少了标题与正文之间的空行），所以固化成用例。
func TestIngestMatchesSharedDocumentVector(t *testing.T) {
	vector := loadVectors(t).Document
	dir := t.TempDir()

	result, err := Ingest(dir, vector.Payload, vector.ClientLabel, &testLogger{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, result.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != vector.ExpectedMarkdown {
		t.Errorf("落盘内容与共用用例不符\n--- 得到 ---\n%s\n--- 期望 ---\n%s", raw, vector.ExpectedMarkdown)
	}

	var want, got map[string]any
	if err := json.Unmarshal(vector.ExpectedIndexEntry, &want); err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(ReadIndex(dir)[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("index 条目与共用用例不符\n得到 %v\n期望 %v", got, want)
	}
}

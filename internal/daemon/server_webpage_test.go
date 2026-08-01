package daemon

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// 验收口径（page-context-design.md §7 P0-c）：本地 curl 直打 ingest 端点即可，
// 不依赖扩展。这组测试就是那条 curl 的自动化版本。
func TestWebPageIngestRoundTrip(t *testing.T) {
	_, ts := newTestServer(t, "")

	body := `{
		"canonicalUrl": "https://example.com/a",
		"url": "https://example.com/a?utm_source=x",
		"title": "Array.prototype.map()",
		"siteName": "MDN Web Docs",
		"capturedAt": "2026-07-27T15:04:22+08:00",
		"markdown": "正文",
		"contentHash": "9f86d081",
		"truncated": false,
		"lowConfidence": false
	}`
	resp, err := http.Post(ts.URL+"/web-pages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var ingest struct {
		OK           bool   `json:"ok"`
		URLHash      string `json:"urlHash"`
		RelativePath string `json:"relativePath"`
		Replaced     bool   `json:"replaced"`
		CaptureCount int    `json:"captureCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ingest); err != nil {
		t.Fatal(err)
	}
	if !ingest.OK || ingest.CaptureCount != 1 || ingest.Replaced {
		t.Fatalf("ingest 响应不对: %+v", ingest)
	}
	if !strings.HasPrefix(ingest.RelativePath, "files/") {
		t.Errorf("relativePath = %q", ingest.RelativePath)
	}

	// 状态回显：扩展 popup 用 16 位前缀查（§3.4 本地索引只存 hash）。
	statusResp, err := http.Get(ts.URL + "/web-pages/status?h=" + url.QueryEscape(ingest.URLHash[:16]) + "&h=deadbeefdeadbeef")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	var status struct {
		Saved map[string]map[string]string `json:"saved"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Saved[ingest.URLHash[:16]]["capturedAt"] != "2026-07-27T15:04:22+08:00" {
		t.Errorf("status 未命中: %+v", status.Saved)
	}
	if _, ok := status.Saved["deadbeefdeadbeef"]; ok {
		t.Error("未收藏的 hash 不应出现在 saved 里")
	}

	// 登录后的一次性回填只要 hash + capturedAt。
	indexResp, err := http.Get(ts.URL + "/web-pages/index?fields=hash,capturedAt")
	if err != nil {
		t.Fatal(err)
	}
	defer indexResp.Body.Close()
	var index struct {
		Pages []map[string]any `json:"pages"`
	}
	if err := json.NewDecoder(indexResp.Body).Decode(&index); err != nil {
		t.Fatal(err)
	}
	if len(index.Pages) != 1 || len(index.Pages[0]) != 2 || index.Pages[0]["urlHash"] != ingest.URLHash {
		t.Fatalf("index 响应不对: %+v", index.Pages)
	}
}

func TestWebPageIngestRejectsOversizedMarkdown(t *testing.T) {
	_, ts := newTestServer(t, "")
	payload, err := json.Marshal(map[string]any{
		"canonicalUrl": "https://example.com/big",
		"markdown":     strings.Repeat("a", 512*1024+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/web-pages", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// 静默截断会让用户以为收全了；超限必须明确报错（§3.5）。
	if resp.StatusCode != 413 {
		t.Fatalf("status = %d, 期望 413", resp.StatusCode)
	}
}

func TestWebPagePathsRequireAuth(t *testing.T) {
	_, ts := newTestServer(t, "secret-token")
	for _, path := range []string{"/web-pages", "/web-pages/status", "/web-pages/index"} {
		if !isIngestPath(path) {
			t.Errorf("%s 应当算 ingest 路径（否则 api-key 持有者只能用一半功能）", path)
		}
	}
	resp, err := http.Post(ts.URL+"/web-pages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("无凭据时 status = %d, 期望 401", resp.StatusCode)
	}
}

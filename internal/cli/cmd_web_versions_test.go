package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/webpage"
)

type quietLogger struct{}

func (quietLogger) Info(string) {}
func (quietLogger) Warn(string) {}

// writeVersionedFixture 用真实的 Ingest 攒出一条三个版本的链，其中 v3 是离线晚到的。
func writeVersionedFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(sandbox(t), "profiles", "default", "web-pages")
	board := func(at, revenue string) webpage.Payload {
		return webpage.Payload{
			CanonicalURL: "https://dashboard.example.com/sales",
			Title:        "销售看板",
			CapturedAt:   at,
			ContentHash:  "h-" + revenue,
			Markdown:     "| 指标 | 值 |\n| --- | --- |\n| 本月营收 | " + revenue + " |\n| 新增客户 | 312 |\n\n数据每小时刷新。\n",
		}
	}
	var last webpage.IngestResult
	for _, capture := range []webpage.Payload{
		board("2026-09-20T09:00:00+08:00", "1150000"),
		board("2026-09-22T09:00:00+08:00", "1284000"),
		board("2026-09-21T09:00:00+08:00", "1210000"), // 晚到
	} {
		result, err := webpage.Ingest(dir, capture, "webext", quietLogger{})
		if err != nil {
			t.Fatal(err)
		}
		last = result
	}
	return dir, last.URLHash
}

func TestWebVersionsListsByCaptureTime(t *testing.T) {
	_, hash := writeVersionedFixture(t)
	out, code := execCLI(t, "synced-web-page", "versions", hash[:16])
	if code != 0 {
		t.Fatalf("versions failed: %s", out)
	}
	result := decode(t, out)
	if result["history"] != true || result["versionCount"] != float64(3) || result["latestVersion"] != float64(3) {
		t.Fatalf("汇总不对: %+v", result)
	}
	var order []float64
	for _, raw := range result["versions"].([]any) {
		v := raw.(map[string]any)
		order = append(order, v["version"].(float64))
		if !strings.HasSuffix(v["path"].(string), ".md") || !filepath.IsAbs(v["path"].(string)) {
			t.Errorf("path 应是绝对路径: %v", v["path"])
		}
		if v["version"] == float64(3) && (v["late"] != true || v["current"] != true) {
			t.Errorf("v3 应标 late 与 current: %+v", v)
		}
	}
	// 按收藏时间：v1(20 日) → v3(21 日，晚到) → v2(22 日)。
	if len(order) != 3 || order[0] != 1 || order[1] != 3 || order[2] != 2 {
		t.Errorf("应按 capturedAt 升序，得到 %v", order)
	}
}

func TestWebDiffDefaultsToLatestAgainstPrevious(t *testing.T) {
	_, hash := writeVersionedFixture(t)
	out, code := execCLI(t, "synced-web-page", "diff", hash)
	if code != 0 {
		t.Fatalf("diff failed: %s", out)
	}
	result := decode(t, out)
	from := result["from"].(map[string]any)
	to := result["to"].(map[string]any)
	if from["version"] != float64(2) || to["version"] != float64(3) {
		t.Fatalf("默认应是 v2 → v3: %+v", result)
	}
	patch := result["patch"].(string)
	if !strings.Contains(patch, "-| 本月营收 | 1284000 |") || !strings.Contains(patch, "+| 本月营收 | 1210000 |") {
		t.Errorf("patch 缺少改动行:\n%s", patch)
	}
	if result["added"] != float64(1) || result["removed"] != float64(1) || result["truncated"] != false {
		t.Errorf("计数不对: %+v", result)
	}

	out, code = execCLI(t, "synced-web-page", "diff", hash, "--from", "v1", "--to", "2")
	if code != 0 || !strings.Contains(decode(t, out)["patch"].(string), "+| 本月营收 | 1284000 |") {
		t.Errorf("--from/--to 不生效: %s", out)
	}
	if _, code := execCLI(t, "synced-web-page", "diff", hash, "--to", "1"); code == 0 {
		t.Error("v1 没有前一版，未指定 --from 时应报错")
	}
	if _, code := execCLI(t, "synced-web-page", "diff", hash, "--from", "9"); code == 0 {
		t.Error("不存在的版本应报错")
	}
}

func TestWebPathVersionAndListFields(t *testing.T) {
	_, hash := writeVersionedFixture(t)
	out, code := execCLI(t, "synced-web-page", "path", hash, "--version", "1")
	if code != 0 {
		t.Fatalf("path --version failed: %s", out)
	}
	result := decode(t, out)
	if !strings.HasSuffix(result["path"].(string), "v000001.md") || result["current"] != false {
		t.Errorf("path --version 1 不对: %+v", result)
	}

	out, _ = execCLI(t, "synced-web-page", "list")
	page := decode(t, out)["pages"].([]any)[0].(map[string]any)
	if page["versionCount"] != float64(3) || page["lastChangedAt"] != "2026-09-22T09:00:00+08:00" {
		t.Errorf("list 缺少版本字段: %+v", page)
	}
}

func TestWebVersionsWithoutHistory(t *testing.T) {
	_, mdn, _ := writeWebFixture(t)
	out, code := execCLI(t, "synced-web-page", "versions", mdn.URLHash)
	if code != 0 {
		t.Fatalf("versions failed: %s", out)
	}
	result := decode(t, out)
	if result["history"] != false || result["versionCount"] != float64(1) {
		t.Errorf("没有链的网页应是单版本视图: %+v", result)
	}
	if _, code := execCLI(t, "synced-web-page", "diff", mdn.URLHash); code == 0 {
		t.Error("没有历史的网页 diff 应报错")
	}
}

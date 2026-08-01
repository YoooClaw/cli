package webpage

import (
	"encoding/json"
	"os"
	"testing"
)

// vectors 是四端共用的用例集。副本必须与
// yc-web-extension/docs/web-page-vectors.json 逐字节一致——规则漂移的后果是
// 同一网页在不同端落成两个文件。
type vectors struct {
	NormalizeURL []struct {
		Name string `json:"name"`
		In   string `json:"in"`
		Out  string `json:"out"`
	} `json:"normalizeUrl"`
	RejectedURLs []string `json:"rejectedUrls"`
	Slug         []struct {
		Name string `json:"name"`
		In   string `json:"in"`
		Out  string `json:"out"`
	} `json:"slug"`
	Document struct {
		ClientLabel        string          `json:"clientLabel"`
		Payload            Payload         `json:"payload"`
		ExpectedMarkdown   string          `json:"expectedMarkdown"`
		ExpectedIndexEntry json.RawMessage `json:"expectedIndexEntry"`
	} `json:"document"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/web-page-vectors.json")
	if err != nil {
		t.Fatalf("读取用例集失败: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析用例集失败: %v", err)
	}
	return v
}

func TestNormalizeURLVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.NormalizeURL) == 0 {
		t.Fatal("用例集为空")
	}
	for _, c := range v.NormalizeURL {
		t.Run(c.Name, func(t *testing.T) {
			got, err := NormalizeURL(c.In)
			if err != nil {
				t.Fatalf("NormalizeURL(%q) 报错: %v", c.In, err)
			}
			if got != c.Out {
				t.Errorf("NormalizeURL(%q) = %q, 期望 %q", c.In, got, c.Out)
			}
		})
	}
}

func TestNormalizeURLIsIdempotent(t *testing.T) {
	for _, c := range loadVectors(t).NormalizeURL {
		once, err := NormalizeURL(c.In)
		if err != nil {
			t.Fatalf("NormalizeURL(%q): %v", c.In, err)
		}
		twice, err := NormalizeURL(once)
		if err != nil {
			t.Fatalf("NormalizeURL(%q): %v", once, err)
		}
		if once != twice {
			t.Errorf("不幂等: %q → %q → %q", c.In, once, twice)
		}
	}
}

func TestNormalizeURLRejects(t *testing.T) {
	for _, raw := range loadVectors(t).RejectedURLs {
		if got, err := NormalizeURL(raw); err == nil {
			t.Errorf("NormalizeURL(%q) 应当拒收，却返回 %q", raw, got)
		}
	}
}

func TestSlugVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Slug) == 0 {
		t.Fatal("slug 用例集为空")
	}
	for _, c := range v.Slug {
		t.Run(c.Name, func(t *testing.T) {
			if got := Slug(c.In); got != c.Out {
				t.Errorf("Slug(%q) = %q, 期望 %q", c.In, got, c.Out)
			}
		})
	}
}

func TestSlugNeverEscapesTheFilesDir(t *testing.T) {
	// slug 拼进 files/<hash8>-<slug>.md，任何路径分隔符都必须被折成连字符。
	for _, title := range []string{"../../etc/passwd", "a/b\\c", "a:b", "\x00nul", "..", "."} {
		got := Slug(title)
		for _, bad := range []string{"/", "\\", ":", "\x00"} {
			if contains(got, bad) {
				t.Errorf("Slug(%q) = %q 含非法字符 %q", title, got, bad)
			}
		}
		if got == "." || got == ".." {
			t.Errorf("Slug(%q) = %q", title, got)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

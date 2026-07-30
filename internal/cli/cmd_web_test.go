package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type testWebPage struct {
	URLHash         string `json:"urlHash"`
	CanonicalURL    string `json:"canonicalUrl"`
	URL             string `json:"url,omitempty"`
	Title           string `json:"title"`
	SiteName        string `json:"siteName,omitempty"`
	RelativePath    string `json:"relativePath"`
	CapturedAt      string `json:"capturedAt"`
	FirstCapturedAt string `json:"firstCapturedAt"`
	CaptureCount    int    `json:"captureCount"`
	Bytes           int    `json:"bytes"`
	ClientLabel     string `json:"clientLabel,omitempty"`
	ArchivePath     string `json:"archivePath,omitempty"`
}

func writeWebFixture(t *testing.T) (string, testWebPage, testWebPage) {
	t.Helper()
	home := sandbox(t)
	dir := filepath.Join(home, "profiles", "default", "web-pages")
	filesDir := filepath.Join(dir, "files")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		t.Fatal(err)
	}

	mdn := testWebPage{
		URLHash:         "36340e4fa4f7c15a111111111111111111111111111111111111111111111111",
		CanonicalURL:    "https://developer.mozilla.org/en-US/docs/Web/JavaScript",
		URL:             "https://developer.mozilla.org/en-US/docs/Web/JavaScript#top",
		Title:           "JavaScript standard built-in objects",
		SiteName:        "MDN Web Docs",
		RelativePath:    "files/36340e4f-javascript.md",
		CapturedAt:      "2026-07-29T10:00:00+08:00",
		FirstCapturedAt: "2026-07-28T10:00:00+08:00",
		CaptureCount:    2,
		Bytes:           320,
		ClientLabel:     "webext",
		ArchivePath:     "files/36340e4f-javascript.html",
	}
	internal := testWebPage{
		URLHash:         "abcdef0123456789222222222222222222222222222222222222222222222222",
		CanonicalURL:    "https://internal.example.com/deployment",
		Title:           "Deployment guide",
		RelativePath:    "files/abcdef01-deployment.md",
		CapturedAt:      "2026-07-28T10:00:00+08:00",
		FirstCapturedAt: "2026-07-28T10:00:00+08:00",
		CaptureCount:    1,
		Bytes:           180,
	}
	index, err := json.Marshal(map[string]any{"pages": []testWebPage{internal, mdn}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, mdn.RelativePath),
		[]byte("---\ntitle: \"frontmatter-only-secret\"\n---\n\n# Standard built-in objects\n\nThis chapter documents all of JavaScript's standard built-in objects.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, internal.RelativePath),
		[]byte("---\ntitle: \"Deployment guide\"\n---\n\n# Deployment guide\n\nUse a rolling update for production services.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, mdn.ArchivePath),
		[]byte("<html><body>html-only-secret</body></html>"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return dir, mdn, internal
}

func TestWebStoragePath(t *testing.T) {
	dir, _, _ := writeWebFixture(t)
	out, code := execCLI(t, "synced-web-page", "storage-path")
	if code != 0 {
		t.Fatalf("synced-web-page storage-path failed: %s", out)
	}
	if got := decode(t, out)["path"]; got != dir {
		t.Errorf("path = %v, want %s", got, dir)
	}
}

func TestWebListNewestFirst(t *testing.T) {
	_, mdn, internal := writeWebFixture(t)
	out, code := execCLI(t, "synced-web-page", "list")
	if code != 0 {
		t.Fatalf("synced-web-page list failed: %s", out)
	}
	result := decode(t, out)
	pages := result["pages"].([]any)
	if result["total"] != float64(2) {
		t.Fatalf("total = %v", result["total"])
	}
	if pages[0].(map[string]any)["urlHash"] != mdn.URLHash ||
		pages[1].(map[string]any)["urlHash"] != internal.URLHash {
		t.Errorf("pages not sorted newest first: %+v", pages)
	}
	if pages[0].(map[string]any)["hasArchive"] != true {
		t.Errorf("hasArchive missing: %+v", pages[0])
	}
}

func TestWebPathAcceptsUniqueEightCharacterPrefix(t *testing.T) {
	dir, mdn, _ := writeWebFixture(t)
	out, code := execCLI(t, "synced-web-page", "path", "36340E4F")
	if code != 0 {
		t.Fatalf("synced-web-page path failed: %s", out)
	}
	if got := decode(t, out)["path"]; got != filepath.Join(dir, mdn.RelativePath) {
		t.Errorf("path = %v", got)
	}
}

func TestWebPathRejectsMissingAndUnsafeEntries(t *testing.T) {
	dir, mdn, internal := writeWebFixture(t)
	if out, code := execCLI(t, "synced-web-page", "path", "deadbeef"); code == 0 {
		t.Fatalf("missing page should fail: %s", out)
	}

	internal.RelativePath = "../../outside.md"
	index, err := json.Marshal(map[string]any{"pages": []testWebPage{internal, mdn}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := execCLI(t, "synced-web-page", "path", internal.URLHash)
	if code == 0 {
		t.Fatalf("unsafe path should fail: %s", out)
	}
	errObj := decode(t, out)["error"].(map[string]any)
	if errObj["code"] != "YOOOCLAW_STORAGE_UNAVAILABLE" {
		t.Errorf("unexpected error: %+v", errObj)
	}
}

func TestWebSearchMetadataAndMarkdownOnly(t *testing.T) {
	_, mdn, internal := writeWebFixture(t)

	out, code := execCLI(t, "synced-web-page", "search", "javascript")
	if code != 0 {
		t.Fatalf("synced-web-page search failed: %s", out)
	}
	result := decode(t, out)
	pages := result["pages"].([]any)
	if result["total"] != float64(1) {
		t.Fatalf("unexpected result: %+v", result)
	}
	page := pages[0].(map[string]any)
	if page["urlHash"] != mdn.URLHash {
		t.Errorf("wrong page: %+v", page)
	}
	fields := page["matchedFields"].([]any)
	if len(fields) != 3 || fields[0] != "title" || fields[1] != "canonicalUrl" || fields[2] != "url" {
		t.Errorf("matchedFields = %+v", fields)
	}

	out, code = execCLI(t, "synced-web-page", "search", "rolling update")
	if code != 0 || decode(t, out)["pages"].([]any)[0].(map[string]any)["urlHash"] != internal.URLHash {
		t.Errorf("body search failed: %s", out)
	}

	for _, keyword := range []string{"html-only-secret", "frontmatter-only-secret"} {
		out, code = execCLI(t, "synced-web-page", "search", keyword)
		if code != 0 || decode(t, out)["total"] != float64(0) {
			t.Errorf("%q should not match archive/frontmatter: %s", keyword, out)
		}
	}
}

func TestWebSearchLimitAndValidation(t *testing.T) {
	_, mdn, _ := writeWebFixture(t)
	out, code := execCLI(t, "synced-web-page", "search", "https", "--limit", "1")
	if code != 0 {
		t.Fatalf("limited search failed: %s", out)
	}
	result := decode(t, out)
	pages := result["pages"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["urlHash"] != mdn.URLHash {
		t.Errorf("limit not honored: %+v", result)
	}

	for _, limit := range []string{"0", "-1", "abc"} {
		out, code = execCLI(t, "synced-web-page", "search", "test", "--limit", limit)
		if code == 0 {
			t.Errorf("--limit %s should fail: %s", limit, out)
		}
	}
}

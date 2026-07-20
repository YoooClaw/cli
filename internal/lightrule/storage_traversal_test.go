package lightrule

import (
	"os"
	"path/filepath"
	"testing"
)

// 回归：name 带路径分隔符不得穿越出 tasks/（Create 写 meta.json、Delete RemoveAll）。
func TestCreateRejectsTraversalName(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"../evil", "..", "a/b", `a\b`, "/abs"} {
		_, err := Create(base, CreateParams{Name: name, Title: "t", Description: "d", Segments: segs()})
		e, ok := err.(*Error)
		if !ok || e.Code != "INVALID_PARAMS" {
			t.Errorf("Create(%q) err = %v, want INVALID_PARAMS", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "evil")); !os.IsNotExist(err) {
		t.Error("traversal create escaped tasks/")
	}
}

func TestResolveDoesNotProbeOutsideTasksDir(t *testing.T) {
	base := t.TempDir()
	// 在 tasks/ 之外放一份合法 meta.json，穿越名不得命中它。
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "meta.json"),
		[]byte(`{"type":"light-rule","name":"outside","segments":[{"mode":"static","color":[255,0,0]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if Get(base, "../outside") != nil {
		t.Error("lookup with ../ escaped tasks/")
	}
	if _, err := Delete(base, "../outside"); err == nil {
		t.Error("Delete with ../ should report NOT_FOUND")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("outside dir must survive traversal delete attempt")
	}
}

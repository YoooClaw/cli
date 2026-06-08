package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestEnsureDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir, 0); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != DirMode {
		t.Errorf("dir mode = %v, want %v", info.Mode().Perm(), DirMode)
	}
	// 幂等
	if err := EnsureDir(dir, DirMode); err != nil {
		t.Fatalf("second EnsureDir: %v", err)
	}
}

func TestWriteAtomicAndExists(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "secret")
	if Exists(path) {
		t.Fatal("should not exist yet")
	}
	if err := WriteAtomic(path, []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if !Exists(path) {
		t.Fatal("should exist after write")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "data" {
		t.Errorf("content = %q", got)
	}
	// 确认没有遗留临时文件
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestWriteJSONReadJSONRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "c.json")
	in := map[string]any{"name": "x", "n": float64(3)}
	if err := WriteJSON(path, in, SecretFileMode); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if raw[len(raw)-1] != '\n' {
		t.Error("WriteJSON should append trailing newline")
	}
	var out map[string]any
	exists, err := ReadJSON(path, &out)
	if err != nil || !exists {
		t.Fatalf("ReadJSON existing: exists=%v err=%v", exists, err)
	}
	if out["name"] != "x" || out["n"] != float64(3) {
		t.Errorf("round trip mismatch: %+v", out)
	}
}

func TestReadJSONMissing(t *testing.T) {
	t.Parallel()
	var out map[string]any
	exists, err := ReadJSON(filepath.Join(t.TempDir(), "nope.json"), &out)
	if exists || err != nil {
		t.Errorf("missing file -> (false,nil), got (%v,%v)", exists, err)
	}
}

func TestReadJSONInvalid(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	exists, err := ReadJSON(path, &out)
	if !exists {
		t.Error("invalid file should report exists=true")
	}
	var ye *errs.Error
	if e, ok := err.(*errs.Error); ok {
		ye = e
	}
	if ye == nil || ye.Code != errs.CodeConfigInvalid {
		t.Errorf("want CONFIG_INVALID, got %v", err)
	}
}

func TestCopyDir(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "b.txt"), []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dst, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(dst, "nested", "b.txt"))
	if string(a) != "A" || string(b) != "B" {
		t.Errorf("copied content mismatch: %q %q", a, b)
	}
}

func TestWriteJSONMarshalError(t *testing.T) {
	t.Parallel()
	// channel 无法 JSON 序列化，应返回错误且不创建文件。
	path := filepath.Join(t.TempDir(), "x.json")
	if err := WriteJSON(path, make(chan int), 0); err == nil {
		t.Error("expected marshal error")
	}
	if Exists(path) {
		t.Error("file should not be created on marshal error")
	}
}

func TestWriteAtomicDirCreateFails(t *testing.T) {
	t.Parallel()
	// 在一个普通文件下面建子路径，EnsureDir 会失败。
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(filepath.Join(blocker, "child", "f"), []byte("y"), 0); err == nil {
		t.Error("expected error writing under a file path")
	}
}

func TestReadJSONReadError(t *testing.T) {
	t.Parallel()
	// 把目录当文件读，os.ReadFile 返回非 NotExist 错误。
	dir := t.TempDir()
	var out map[string]any
	exists, err := ReadJSON(dir, &out)
	if err == nil {
		t.Error("reading a directory should error")
	}
	_ = exists
}

func TestCopyDirSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	t.Parallel()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "target"), []byte("T"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil || link != "target" {
		t.Errorf("symlink not preserved: %q err=%v", link, err)
	}
}

func TestCopyDirMissingSource(t *testing.T) {
	t.Parallel()
	if err := CopyDir(filepath.Join(t.TempDir(), "nope"), t.TempDir()); err == nil {
		t.Error("copying missing source should error")
	}
}

func TestCountFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "d1"), 0o700)
	os.WriteFile(filepath.Join(root, "f1"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "d1", "f2"), []byte("y"), 0o600)
	// 条目：d1, f1, d1/f2 = 3
	if got := CountFiles(root); got != 3 {
		t.Errorf("CountFiles = %d, want 3", got)
	}
}

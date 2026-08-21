package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExistingRegularFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.MkdirAll(filepath.Join(root, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside", "audio.wav")
	if err := os.WriteFile(inside, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, ok := ResolveExistingRegularFile(root, "inside/audio.wav")
	expected, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || resolved != expected {
		t.Fatalf("inside file = %q, %v", resolved, ok)
	}
	for _, unsafe := range []string{"", outside, "../outside.txt"} {
		if resolved, ok := ResolveExistingRegularFile(root, unsafe); ok {
			t.Fatalf("unsafe path %q resolved to %q", unsafe, resolved)
		}
	}
	if resolved, ok := ResolveExistingRegularFile(root, "inside"); ok {
		t.Fatalf("directory resolved as file: %q", resolved)
	}

	symlink := filepath.Join(root, "inside", "escape")
	if err := os.Symlink(outside, symlink); err == nil {
		if resolved, ok := ResolveExistingRegularFile(root, "inside/escape"); ok {
			t.Fatalf("escaping symlink resolved to %q", resolved)
		}
	}
}

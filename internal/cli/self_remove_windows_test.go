//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRemovalCandidatesPutCurrentExecutableLast(t *testing.T) {
	t.Parallel()

	exe := `C:\Users\tester\AppData\Local\YoooClaw\bin\yc.exe`
	got := windowsRemovalCandidates(exe)
	if len(got) != 2 {
		t.Fatalf("windowsRemovalCandidates() len = %d, want 2: %v", len(got), got)
	}
	if !strings.EqualFold(got[0], filepath.Join(filepath.Dir(exe), "yoooclaw.exe")) {
		t.Errorf("first candidate = %q, want yoooclaw.exe", got[0])
	}
	if !strings.EqualFold(got[1], exe) {
		t.Errorf("last candidate = %q, want current executable %q", got[1], exe)
	}
}

func TestRemoveWindowsPathNowRemovesPathSynchronously(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "disposable.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeWindowsPathNow(path); err != nil {
		t.Fatalf("removeWindowsPathNow() error = %v", err)
	}
	if err := verifyWindowsPathRemoved(path); err != nil {
		t.Fatalf("path was not removed synchronously: %v", err)
	}
}

func TestRemoveWindowsPathEntryRemovesOnlyMatchingEntries(t *testing.T) {
	t.Parallel()

	target := `C:\Users\tester\AppData\Local\YoooClaw\bin`
	input := `C:\Tools; C:\USERS\TESTER\AppData\Local\YoooClaw\bin\ ;D:\SDK;` + target
	want := `C:\Tools;D:\SDK`
	got, changed := removeWindowsPathEntry(input, target)
	if !changed {
		t.Fatal("removeWindowsPathEntry() changed = false, want true")
	}
	if got != want {
		t.Fatalf("removeWindowsPathEntry() = %q, want %q", got, want)
	}

	unchanged := `C:\Tools;D:\SDK`
	if got, changed := removeWindowsPathEntry(unchanged, target); changed || got != unchanged {
		t.Fatalf("unrelated PATH changed: got=%q changed=%v", got, changed)
	}
}

func TestRemoveEmptyWindowsInstallDirs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "YoooClaw")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := removeEmptyWindowsInstallDirs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed dirs = %v, want bin and YoooClaw root", removed)
	}
	for _, path := range []string{bin, root} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("directory still exists: %s (%v)", path, err)
		}
	}
}

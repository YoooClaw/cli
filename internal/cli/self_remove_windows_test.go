//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRestoreWindowsAliasesAfterRemovalFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := filepath.Join(dir, "yoooclaw.exe")
	alias := filepath.Join(dir, "yc.exe")
	want := []byte("executable fixture")
	if err := os.WriteFile(exe, want, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restoreWindowsAliases(exe, []string{alias}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("restored alias = %q, want %q", got, want)
	}
}

func TestWindowsDeferredRemovalCommandUsesEnvironmentPath(t *testing.T) {
	t.Parallel()
	command := windowsDeferredRemovalCommand()
	for _, want := range []string{"YOOOCLAW_UNINSTALL_PENDING_PATH", "YOOOCLAW_UNINSTALL_PENDING_ROOT", "del /F /Q", "if exist"} {
		if !strings.Contains(command, want) {
			t.Fatalf("cleanup command missing %q: %s", want, command)
		}
	}
	if strings.Contains(command, "powershell") || strings.Contains(command, "EncodedCommand") {
		t.Fatal("cleanup command must not depend on PowerShell")
	}
}

func TestWindowsRemovalHelperDeletesPendingFile(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "yoooclaw-test.exe.pending")
	if err := os.WriteFile(pending, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startWindowsRemovalHelper(pending, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pending); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cleanup helper left pending file %s", pending)
}

func TestWindowsRemovalHelperDeletesRunningExecutable(t *testing.T) {
	const childEnv = "YOOOCLAW_TEST_RUNNING_SELF_DELETE"
	if os.Getenv(childEnv) == "1" {
		if err := startWindowsRemovalHelper(os.Args[0], true); err != nil {
			t.Fatalf("startWindowsRemovalHelper() error = %v", err)
		}
		// Keep the image mapped long enough to force the helper through its
		// retry path before this process exits.
		time.Sleep(2 * time.Second)
		return
	}

	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(t.TempDir(), "yoooclaw-running.exe")
	if err := os.WriteFile(pending, data, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pending, "-test.run=^TestWindowsRemovalHelperDeletesRunningExecutable$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("self-delete child failed: %v output=%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pending); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("cleanup helper left the exited executable %s", pending)
}

func TestWindowsRemovalCommandRunsSynchronously(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "yoooclaw-sync.exe.pending")
	if err := os.WriteFile(pending, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newWindowsRemovalHelperCommand(pending, false)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cleanup command failed: %v output=%s", err, out)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("cleanup command left pending file: %v", err)
	}
}

func TestWindowsCleanupRemovesOnlyStalePendingExecutables(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pending := filepath.Join(root, "yoooclaw-old.exe.pending")
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(pending, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if warnings := cleanupStaleWindowsUninstallFilesIn(root); len(warnings) != 0 {
		t.Fatalf("cleanup warnings = %v", warnings)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending file still exists: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
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

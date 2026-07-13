//go:build !windows

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestProbeFileLockMissingFile(t *testing.T) {
	held, err := probeFileLock(filepath.Join(t.TempDir(), "writer.lock"))
	if err != nil || held {
		t.Fatalf("missing lock file should be not-held, got held=%v err=%v", held, err)
	}
}

func TestProbeFileLockHeldAndReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer.lock")
	if err := os.WriteFile(path, []byte(`{"owner":"hermes-plugin","pid":123}`), 0o644); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	// flock 以 open file description 为粒度：同进程另一个 fd 也会冲突，
	// 因此可以进程内模拟"插件持锁"。
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	held, err := probeFileLock(path)
	if err != nil || !held {
		t.Fatalf("expected held=true, got held=%v err=%v", held, err)
	}

	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	held, err = probeFileLock(path)
	if err != nil || held {
		t.Fatalf("expected held=false after unlock, got held=%v err=%v", held, err)
	}
}

func TestCheckWriterLockReturnsStructuredError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "writer.lock")
	if err := os.WriteFile(path, []byte(`{"owner":"hermes-plugin","pid":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	checkErr := checkWriterLock(paths.Paths{Dir: dir})
	if checkErr == nil {
		t.Fatal("expected error while writer lock is held")
	}
	var cliErr *errs.Error
	if !errors.As(checkErr, &cliErr) || cliErr.Code != errs.CodeDaemonDisabledByPlugin {
		t.Fatalf("expected %s, got %v", errs.CodeDaemonDisabledByPlugin, checkErr)
	}
}

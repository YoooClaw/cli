//go:build !windows

package yclib_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/YoooClaw/cli/yclib"
)

func TestDaemonStart_ReturnsPluginWriterLockConflictWithoutForking(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "profiles", "default")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(profileDir, "writer.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lockFile.Close() })
	if _, err := lockFile.WriteString(`{"owner":"hermes-plugin","pid":4242}`); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) })

	client, err := yclib.New(yclib.Config{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	_, err = client.Daemon().Start(context.Background(), yclib.DaemonStartOpts{})
	if code := yclib.ErrorCode(err); code != yclib.CodeDaemonDisabledByPlugin {
		t.Fatalf("ErrorCode = %q, want %q; err=%v", code, yclib.CodeDaemonDisabledByPlugin, err)
	}
	if !strings.Contains(err.Error(), "hermes-plugin (pid 4242)") {
		t.Fatalf("error should identify the writer-lock owner, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("writer-lock conflict should fail before fork, took %s", elapsed)
	}
	if _, statErr := os.Stat(filepath.Join(profileDir, "daemon.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("daemon.lock should not be created, stat err=%v", statErr)
	}
}

package daemon

import (
	"os"
	"testing"

	"github.com/YoooClaw/cli/internal/paths"
)

func sandboxPaths(t *testing.T) paths.Paths {
	t.Helper()
	t.Setenv("YOOOCLAW_HOME", t.TempDir())
	p := paths.For(paths.DefaultProfile)
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLockRoundTrip(t *testing.T) {
	p := sandboxPaths(t)
	if ReadLock(p) != nil {
		t.Fatal("no lock should exist initially")
	}
	if State(p).Running {
		t.Error("no lock -> not running")
	}
	lock := Lock{PID: os.Getpid(), StartedAt: "2026-06-07T10:00:00Z", Bind: "127.0.0.1", Port: 18789}
	if err := WriteLock(p, lock); err != nil {
		t.Fatal(err)
	}
	got := ReadLock(p)
	if got == nil || got.PID != os.Getpid() || got.Port != 18789 {
		t.Fatalf("lock round trip: %+v", got)
	}
	// 自己的 pid 必然存活 -> Running
	st := State(p)
	if !st.Running || st.Stale {
		t.Errorf("self pid should be running: %+v", st)
	}
	RemoveLock(p)
	if ReadLock(p) != nil {
		t.Error("lock should be removed")
	}
}

func TestStateStaleLock(t *testing.T) {
	p := sandboxPaths(t)
	// 一个几乎不可能存活的 pid
	if err := WriteLock(p, Lock{PID: 2147483600, Port: 1}); err != nil {
		t.Fatal(err)
	}
	st := State(p)
	if st.Running || !st.Stale {
		t.Errorf("dead pid lock should be stale: %+v", st)
	}
}

func TestIsProcessAlive(t *testing.T) {
	if !IsProcessAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if IsProcessAlive(2147483600) {
		t.Error("absurd pid should not be alive")
	}
}

func TestAssertRunning(t *testing.T) {
	p := sandboxPaths(t)
	if AssertRunning(p) == nil {
		t.Error("no daemon -> AssertRunning should error")
	}
	WriteLock(p, Lock{PID: os.Getpid(), Port: 1})
	if err := AssertRunning(p); err != nil {
		t.Errorf("self-pid lock -> running: %v", err)
	}
}

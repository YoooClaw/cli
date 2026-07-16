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
	lock := Lock{
		PID: os.Getpid(), StartedAt: "2026-06-07T10:00:00Z", Bind: "127.0.0.1", Port: 18789,
		Owner: "hermes-plugin", Generation: "gen-1", Executable: "/tmp/yoooclaw", Version: "1.2.3", Profile: "default",
	}
	if err := WriteLock(p, lock); err != nil {
		t.Fatal(err)
	}
	got := ReadLock(p)
	if got == nil || got.PID != os.Getpid() || got.Port != 18789 {
		t.Fatalf("lock round trip: %+v", got)
	}
	if got.Owner != "hermes-plugin" || got.Generation != "gen-1" || got.Executable != "/tmp/yoooclaw" || got.Version != "1.2.3" || got.Profile != "default" {
		t.Fatalf("lock lifecycle fields not preserved: %+v", got)
	}
	// 当前 test binary 不是 daemon，不能拿上面的伪 executable/cmdline 做身份
	// 校验；另写一个 legacy lock 单独验证基础 PID 判活。
	if err := WriteLock(p, Lock{PID: os.Getpid(), Bind: "127.0.0.1", Port: 18789}); err != nil {
		t.Fatal(err)
	}
	st := State(p)
	if !st.Running || st.Stale {
		t.Errorf("self pid should be running: %+v", st)
	}
	RemoveLock(p)
	if ReadLock(p) != nil {
		t.Error("lock should be removed")
	}
}

func TestRemoveLockIfOwnedBy(t *testing.T) {
	p := sandboxPaths(t)
	if err := WriteLock(p, Lock{PID: 4242, Port: 1}); err != nil {
		t.Fatal(err)
	}
	// 迟退出的旧进程（pid 不同）不能删掉新 daemon 的锁。
	RemoveLockIfOwnedBy(p, 1111)
	if ReadLock(p) == nil {
		t.Fatal("lock owned by another pid must survive")
	}
	RemoveLockIfOwnedBy(p, 4242)
	if ReadLock(p) != nil {
		t.Fatal("owner pid should remove the lock")
	}
}

func TestStopRejectsLifecycleMismatch(t *testing.T) {
	p := sandboxPaths(t)
	if err := WriteLock(p, Lock{PID: os.Getpid(), Owner: "hermes-plugin", Generation: "gen-1", Port: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := StopWithOptions(p, StopOpts{Owner: "other", Wait: true}); err == nil {
		t.Fatal("owner mismatch should fail")
	}
	if _, err := StopWithOptions(p, StopOpts{Owner: "hermes-plugin", Generation: "gen-2", Wait: true}); err == nil {
		t.Fatal("generation mismatch should fail")
	}
	RemoveLock(p)
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

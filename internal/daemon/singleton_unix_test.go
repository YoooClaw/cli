//go:build !windows

package daemon

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireProcessLockExclusive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), daemonSingletonName)

	release, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// flock 绑定在 open file description 上，二次 open 即使同进程也会冲突。
	if _, err := acquireProcessLock(path); !errors.Is(err, errProcessLockHeld) {
		t.Fatalf("second acquire should report held, got %v", err)
	}
	release()
	release2, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}

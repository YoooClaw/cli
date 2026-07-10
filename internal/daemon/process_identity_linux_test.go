//go:build linux

package daemon

import (
	"os"
	"testing"
)

func TestStateRejectsReusedLinuxPID(t *testing.T) {
	p := sandboxPaths(t)
	if err := WriteLock(p, Lock{PID: os.Getpid(), Executable: "/definitely/not/the/test-binary"}); err != nil {
		t.Fatal(err)
	}
	st := State(p)
	if st.Running || !st.Stale {
		t.Fatalf("reused PID should be stale: %+v", st)
	}
}

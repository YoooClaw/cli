//go:build linux

package daemon

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
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

func TestManagedProcessIdentityHelper(t *testing.T) {
	if os.Getenv("YOOOCLAW_IDENTITY_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestStateRecognizesManagedLinuxDaemon(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"run-service", "run-foreground", "status"} {
		t.Run(command, func(t *testing.T) {
			child := exec.Command(exe, "-test.run=^TestManagedProcessIdentityHelper$", "--", "daemon", command, "--root", t.TempDir())
			child.Env = append(os.Environ(), "YOOOCLAW_IDENTITY_HELPER=1")
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("helper: %q %v", line, err)
			}
			p := sandboxPaths(t)
			if err := WriteLock(p, Lock{PID: child.Process.Pid, Executable: exe}); err != nil {
				t.Fatal(err)
			}
			state := State(p)
			want := command != "status"
			if state.Running != want || state.Stale == want {
				actualExe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", child.Process.Pid))
				cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", child.Process.Pid))
				t.Fatalf("%s state: %+v; expected executable=%q actual=%q cmdline=%q", command, state, exe, actualExe, cmdline)
			}
		})
	}
}

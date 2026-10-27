//go:build linux

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/autostart"
)

func TestDaemonServiceCgroup(t *testing.T) {
	root := t.TempDir()
	unit := autostart.ServiceID(root) + ".service"
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"0::/user.slice/user-1000.slice/user@1000.service/app.slice/" + unit, true},
		{"1:name=systemd:/user.slice/" + unit + "/child", true},
		{"0::/user.slice/session-1.scope", false},
		{"0::/app.slice/other-" + unit, false},
	} {
		if got := inDaemonService(tc.value, root); got != tc.want {
			t.Fatalf("%q: %v", tc.value, got)
		}
	}
}
func TestSystemdWorkerCommand(t *testing.T) {
	a := Attempt{UpdateID: strings.Repeat("a", 32), Identity: Identity{Root: "/tmp/user data/100%", Profile: "default"}}
	cmd := exec.Command("/tmp/worker", "--profile", "default", "update", "_apply", JobPath(a))
	args := systemdWorkerArgs(cmd, a)
	joined := strings.Join(args, "\n")
	for _, want := range []string{"--user", "--collect", "--service-type=exec", "--unit=yoooclaw-update-" + a.UpdateID, "--setenv=YOOOCLAW_HOME=" + a.Identity.Root, "100%%"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--scope") || strings.Contains(joined, "--wait") {
		t.Fatal("must create independent service without waiting for update")
	}
}

// Explicit opt-in: run this test inside an isolated yoooclaw systemd unit.
// The launched worker stops that unit and then writes a marker outside it.
func TestSystemdWorkerIntegration(t *testing.T) {
	root := os.Getenv("YOOOCLAW_WORKER_INTEGRATION_ROOT")
	if root == "" {
		t.Skip("requires isolated systemd user service")
	}
	a := Attempt{UpdateID: strings.Repeat("b", 32), Identity: Identity{Root: root, Profile: "default"}}
	if err := os.MkdirAll(WorkDir(a), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "probe.sh")
	unit := autostart.ServiceID(root) + ".service"
	// No production daemon or credentials are involved.
	content := "#!/bin/sh\nset -eu\nsystemctl --user stop '" + unit + "'\nprintf survived > '" + filepath.Join(root, "survived") + "'\n"
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script)
	cmd.Env = os.Environ()
	if err := startWorker(cmd, a); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
	t.Fatal("source unit was not stopped")
}

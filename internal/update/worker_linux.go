//go:build linux

package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/autostart"
)

func inDaemonService(cgroup, root string) bool {
	unit := autostart.ServiceID(root) + ".service"
	for _, line := range strings.Split(cgroup, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		for _, part := range strings.Split(fields[2], "/") {
			if part == unit {
				return true
			}
		}
	}
	return false
}

func systemdWorkerArgs(cmd *exec.Cmd, a Attempt) []string {
	logPath := strings.ReplaceAll(filepath.Join(WorkDir(a), "worker.log"), "%", "%%")
	args := []string{"--user", "--quiet", "--collect", "--service-type=exec",
		"--unit=yoooclaw-update-" + a.UpdateID,
		"--property=UMask=0077",
		"--property=StandardOutput=append:" + logPath,
		"--property=StandardError=append:" + logPath,
		"--setenv=YOOOCLAW_HOME=" + a.Identity.Root,
	}
	// Keep command discovery for daemon lifecycle and skill installation. Credentials
	// remain in the existing state directory, never in systemd-run's arguments.
	if path := os.Getenv("PATH"); path != "" {
		args = append(args, "--setenv=PATH="+path)
	}
	return append(append(args, "--", cmd.Path), cmd.Args[1:]...)
}

func startWorker(cmd *exec.Cmd, a Attempt) error {
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("cannot determine update worker cgroup: %w", err)
	}
	if !inDaemonService(string(cgroup), a.Identity.Root) {
		return startDetachedWorker(cmd, a)
	}
	// setsid does not leave a systemd cgroup. Launch a separate service before the
	// worker is allowed to stop the daemon; never fall back into the old cgroup.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	launcher := exec.CommandContext(ctx, "systemd-run", systemdWorkerArgs(cmd, a)...)
	launcher.Env = cmd.Env
	if output, err := launcher.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot start independent update service: %w (%s)", err, safe(string(output)))
	}
	return nil
}

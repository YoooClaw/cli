package daemon

import (
	"encoding/json"
	"os"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/paths"
)

// embeddedDaemonEnv is set only on a child process explicitly re-executed by
// yclib's Daemon().Start. The yclib package init hook consumes it before the
// embedding host's main function runs.
const embeddedDaemonEnv = "YOOOCLAW_YCLIB_DAEMON_BOOTSTRAP"

type embeddedLaunch struct {
	Root    string      `json:"root"`
	Profile string      `json:"profile"`
	Paths   paths.Paths `json:"paths"`
	Opts    StartOpts   `json:"opts"`
}

// RunEmbeddedIfRequested runs the daemon in a re-executed yclib host process.
// requested is false for every ordinary library import. When true, this call
// normally blocks for the daemon lifetime.
func RunEmbeddedIfRequested() (requested bool, err error) {
	raw, requested := os.LookupEnv(embeddedDaemonEnv)
	if !requested {
		return false, nil
	}
	_ = os.Unsetenv(embeddedDaemonEnv)

	var launch embeddedLaunch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return true, err
	}
	if launch.Root != "" {
		_ = os.Setenv("YOOOCLAW_HOME", launch.Root)
	}
	ctx := &clictx.Context{Profile: launch.Profile, Paths: launch.Paths}
	return true, RunForeground(ctx, launch.Opts)
}

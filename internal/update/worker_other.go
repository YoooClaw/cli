//go:build !linux

package update

import "os/exec"

func startWorker(cmd *exec.Cmd, a Attempt) error { return startDetachedWorker(cmd, a) }

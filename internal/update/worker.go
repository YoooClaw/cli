package update

import (
	"os"
	"os/exec"
	"path/filepath"
)

func startDetachedWorker(cmd *exec.Cmd, a Attempt) error {
	detach(cmd)
	log, err := os.OpenFile(filepath.Join(WorkDir(a), "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

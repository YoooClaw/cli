package yclib

import (
	"os"

	"github.com/YoooClaw/cli/internal/daemon"
)

// A process re-executed by Daemon().Start enters the daemon before control can
// reach the embedding application's main function. Ordinary imports are a
// no-op because the private bootstrap environment variable is absent.
func init() {
	requested, err := daemon.RunEmbeddedIfRequested()
	if !requested {
		return
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

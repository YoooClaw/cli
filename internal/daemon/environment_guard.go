package daemon

import (
	"path/filepath"

	"github.com/YoooClaw/cli/internal/paths"
)

// AcquireControlLock serializes CLI environment changes and explicit owner handoffs.
func AcquireControlLock() (func(), error) {
	return acquireProcessLock(filepath.Join(paths.RootDir(), "runtime-control.flock"))
}

// ReserveRelay prevents another runtime claiming the account while offline config is changed.
func ReserveRelay(p paths.Paths) (func(), error) { return acquireRelayConsumerLock(p) }

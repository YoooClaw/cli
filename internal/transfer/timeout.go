package transfer

import (
	"os"
	"time"
)

// Timeout is configured independently in the CLI and daemon environments.
// Six hours is provisional pending the 10 GB Wuying performance test.
func Timeout() time.Duration {
	if v, e := time.ParseDuration(os.Getenv("YOOOCLAW_TRANSFER_TIMEOUT")); e == nil && v > 0 {
		return v
	}
	return 6 * time.Hour
}

package cli

import (
	"fmt"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestExitCodeOfWrappedError(t *testing.T) {
	e := errs.New("YOOOCLAW_TRANSFER_CLOUD_UPLOAD_FAILED", "public message")
	e.ExitCode = 17
	if got := exitCodeOf(fmt.Errorf("wrapper: %w", e)); got != 17 {
		t.Fatalf("exit code=%d", got)
	}
	if got := exitCodeOf(fmt.Errorf("plain")); got != 1 {
		t.Fatalf("fallback=%d", got)
	}
}

//go:build !windows

package daemon

import (
	"errors"
	"os"
	"testing"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestRelayConsumerLockIsSharedAcrossProfiles(t *testing.T) {
	root := t.TempDir()
	first := paths.ForRoot(root, "test")
	second := paths.ForRoot(root, "default")
	if err := os.MkdirAll(first.Dir, 0o700); err != nil {
		t.Fatal(err)
	}

	release, err := acquireRelayConsumerLock(first)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	err = checkRelayConsumerLock(second)
	var structured *errs.Error
	if !errors.As(err, &structured) || structured.Code != errs.CodeDaemonAlreadyRunning {
		t.Fatalf("second profile should be rejected with daemon conflict, got %v", err)
	}
}

func TestRelayConsumerLockOnlyAppliesToEnabledStandalone(t *testing.T) {
	cfg := config.Config{Relay: config.RelaySection{Enabled: true}}
	if !needsRelayConsumerLock(config.IngressStandalone, cfg) {
		t.Fatal("enabled standalone daemon must take account relay lock")
	}
	if needsRelayConsumerLock(config.IngressDirect, cfg) || needsRelayConsumerLock(config.IngressProxied, cfg) {
		t.Fatal("direct/proxied daemon must not take account relay lock")
	}
	cfg.Relay.Enabled = false
	if needsRelayConsumerLock(config.IngressStandalone, cfg) {
		t.Fatal("standalone daemon with relay disabled must not take account relay lock")
	}
}

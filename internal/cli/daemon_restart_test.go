package cli

import (
	"errors"
	"testing"
)

func TestRestartReleasesCurrentProfileBeforePrecheck(t *testing.T) {
	held := true
	stopped := false
	err := reconcileManagedRestart(true, func() error {
		stopped = true
		held = false
		return nil
	}, func() error {
		if held {
			return errors.New("YOOOCLAW_DAEMON_ALREADY_RUNNING")
		}
		return nil
	})
	if err != nil || !stopped {
		t.Fatalf("restart blocked by its own daemon: stopped=%v err=%v", stopped, err)
	}
}

func TestRestartPreservesOwnershipAndStopFailures(t *testing.T) {
	denied := errors.New("Relay belongs to another profile")
	stopFailed := errors.New("daemon did not exit")
	for _, tc := range []struct {
		name                    string
		running                 bool
		stopErr, checkErr, want error
		wantStops, wantChecks   int
	}{
		{"other profile", false, nil, denied, denied, 0, 1},
		{"stop failed", true, stopFailed, nil, stopFailed, 1, 0},
		{"lock still held after stop", true, nil, denied, denied, 1, 1},
		{"no daemon", false, nil, nil, nil, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stops, checks := 0, 0
			err := reconcileManagedRestart(tc.running, func() error { stops++; return tc.stopErr }, func() error { checks++; return tc.checkErr })
			if !errors.Is(err, tc.want) || stops != tc.wantStops || checks != tc.wantChecks {
				t.Fatalf("err=%v stops=%d checks=%d", err, stops, checks)
			}
		})
	}
}

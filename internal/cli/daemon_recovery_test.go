package cli

import (
	"errors"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestWindowsTaskRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, platform, code string
		fallback             bool
	}{
		{"disabled task", "windows", errs.CodeAutostartUnavailable, true},
		{"task readiness timeout", "windows", errs.CodeDaemonNotRunning, true},
		{"owner conflict", "windows", errs.CodeDaemonDisabledByPlugin, false},
		{"configuration error", "windows", errs.CodeConfigInvalid, false},
		{"linux unchanged", "linux", errs.CodeAutostartUnavailable, false},
		{"mac unchanged", "darwin", errs.CodeAutostartUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := errs.New(tc.code, "任务被禁用 HRESULT:0x80041326")
			reported, spawned := 0, 0
			got, err := recoverWindowsTask(tc.platform, original, func() { reported++ }, func() (any, error) {
				if reported != 1 {
					t.Fatal("must record error before attempting recovery")
				}
				spawned++
				return "running", nil
			})
			if tc.fallback {
				if err != nil || got != "running" || reported != 1 || spawned != 1 {
					t.Fatalf("%v %v %d %d", got, err, reported, spawned)
				}
			} else if err != original || reported != 0 || spawned != 0 {
				t.Fatal("unexpected fallback", err)
			}
		})
	}
	failure := errors.New("owner changed before spawn")
	_, err := recoverWindowsTask("windows", errs.New(errs.CodeAutostartUnavailable, "disabled"), func() {}, func() (any, error) { return nil, failure })
	if err != failure {
		t.Fatal("fallback failure must propagate", err)
	}
}

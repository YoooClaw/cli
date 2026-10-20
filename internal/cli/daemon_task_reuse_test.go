package cli

import (
	"errors"
	"runtime"
	"testing"

	"github.com/YoooClaw/cli/internal/autostart"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestManagedStartAndRestartRegistrationByPlatform(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "restart"}[restart], func(t *testing.T) {
			root := sandbox(t)
			manager := autostartManager()
			const registeredExecutable = "/existing/registered/yoooclaw"
			if _, err := autostart.Enable(manager, autostart.Spec{RootDir: root, Executable: registeredExecutable}, false); err != nil {
				t.Fatal(err)
			}
			ctx := &clictx.Context{Profile: "default", Paths: paths.For("default")}
			if restart {
				if _, err := restartManagedDaemon(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := startStandalone(ctx); err != nil {
					t.Fatal(err)
				}
			}
			status, err := manager.Status()
			if err != nil {
				t.Fatal(err)
			}
			if !status.Running {
				t.Fatalf("service did not start: %+v", status)
			}
			if runtime.GOOS == "windows" {
				if status.Executable != registeredExecutable {
					t.Fatalf("Windows task was rewritten: %+v", status)
				}
			} else if status.Executable == registeredExecutable {
				t.Fatalf("non-Windows service registration was not refreshed: %+v", status)
			}
		})
	}
}

// Model the user's task: readable and runnable, but registration is denied.
type registrationDeniedManager struct {
	autostart.Manager
	installs int
}

func (m *registrationDeniedManager) Install(spec autostart.Spec) error {
	m.installs++
	return errors.New("E_ACCESSDENIED")
}

func TestManagedStartRegistrationPermissions(t *testing.T) {
	for _, tc := range []struct {
		name, platform              string
		installed, wantRegistration bool
	}{
		{"windows existing", "windows", true, false},
		{"windows missing", "windows", false, true},
		{"linux existing", "linux", true, true},
		{"mac existing", "darwin", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			m := &registrationDeniedManager{Manager: autostartManager()}
			err := prepareManagedStart(m, autostart.Status{Installed: tc.installed}, tc.platform)
			if tc.wantRegistration {
				if err == nil || m.installs != 1 {
					t.Fatalf("expected registration failure: calls=%d err=%v", m.installs, err)
				}
			} else if err != nil || m.installs != 0 {
				t.Fatalf("existing Windows task must not require registration: calls=%d err=%v", m.installs, err)
			}
		})
	}
}

func TestManagedStartRegistersMissingWindowsTask(t *testing.T) {
	sandbox(t)
	m := autostartManager()
	if err := prepareManagedStart(m, autostart.Status{}, "windows"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Running {
		t.Fatalf("missing task was not installed and started: %+v", status)
	}
}

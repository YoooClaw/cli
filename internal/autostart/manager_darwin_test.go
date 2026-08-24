//go:build darwin

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeLaunchctlExitError struct{ code int }

func (e fakeLaunchctlExitError) Error() string { return "launchctl failed" }
func (e fakeLaunchctlExitError) ExitCode() int { return e.code }

func stubLaunchctl(t *testing.T, loaded *bool, bootoutErr error, unloadOnBootout bool) *int {
	t.Helper()
	original := launchctl
	bootoutCalls := 0
	pendingUnload := false
	launchctl = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "print":
			if pendingUnload {
				// launchd may still expose the service for one observation after
				// bootout returns, then converge to the unloaded state.
				pendingUnload = false
				*loaded = false
				return []byte("state = running\npid = 123\n"), nil
			}
			if !*loaded {
				if strings.Count(args[1], "/") == 1 {
					return []byte("services = {\n}\n"), nil
				}
				return []byte("service missing"), fakeLaunchctlExitError{code: 113}
			}
			return []byte("state = running\npid = 123\n"), nil
		case "bootout":
			bootoutCalls++
			if unloadOnBootout {
				pendingUnload = true
			}
			return []byte("bootout result"), bootoutErr
		default:
			return nil, errors.New("unexpected launchctl command")
		}
	}
	t.Cleanup(func() { launchctl = original })
	return &bootoutCalls
}

func TestPlistUsesArgumentArrayAndEscapesValues(t *testing.T) {
	spec := Spec{
		RootDir:       "/tmp/root & profile",
		Executable:    "/tmp/Yooo Claw/yoooclaw",
		Arguments:     []string{"daemon", "run-service", "--root", "/tmp/root & profile"},
		SupervisorLog: "/tmp/root & profile/logs/supervisor.log",
	}
	xml := plistXML("com.yoooclaw.test", spec)
	for _, want := range []string{
		"<key>ProgramArguments</key>",
		"<string>/tmp/Yooo Claw/yoooclaw</string>",
		"<string>/tmp/root &amp; profile</string>",
		"<key>SuccessfulExit</key><false/>",
	} {
		if !strings.Contains(xml, want) {
			t.Fatalf("plist missing %q:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "sh -c") {
		t.Fatal("plist must not execute through a shell")
	}
}

func TestDarwinStopAcceptsBootoutErrorWhenServiceIsGone(t *testing.T) {
	loaded := true
	bootoutCalls := stubLaunchctl(t, &loaded, fakeLaunchctlExitError{code: 3}, true)
	m := &platformManager{label: "com.yoooclaw.test", plist: filepath.Join(t.TempDir(), "test.plist")}

	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second stop failed: %v", err)
	}
	if *bootoutCalls != 1 {
		t.Fatalf("bootout calls = %d", *bootoutCalls)
	}
}

func TestDarwinStopReturnsErrorWhenServiceRemainsLoaded(t *testing.T) {
	loaded := true
	stubLaunchctl(t, &loaded, fakeLaunchctlExitError{code: 3}, false)
	m := &platformManager{label: "com.yoooclaw.test", plist: filepath.Join(t.TempDir(), "test.plist")}

	if err := m.Stop(); err == nil {
		t.Fatal("expected stop error while service remains loaded")
	}
}

func TestDarwinUninstallStopsOnceAndIsIdempotent(t *testing.T) {
	loaded := true
	bootoutCalls := stubLaunchctl(t, &loaded, nil, true)
	plist := filepath.Join(t.TempDir(), "test.plist")
	if err := os.WriteFile(plist, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &platformManager{label: "com.yoooclaw.test", plist: plist}

	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
	if *bootoutCalls != 1 {
		t.Fatalf("bootout calls = %d", *bootoutCalls)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist still exists or stat failed unexpectedly: %v", err)
	}
}

//go:build linux

package autostart

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type fakeSystemd struct {
	loaded       bool
	running      bool
	stopChanges  bool
	stopErr      error
	stopCalls    int
	disableCalls int
}

func stubSystemctl(t *testing.T, m *platformManager, fake *fakeSystemd) {
	t.Helper()
	original := systemctl
	originalLogin := loginctl
	loginctl = func(args ...string) ([]byte, error) { return []byte("yes"), nil }
	t.Cleanup(func() { loginctl = originalLogin })
	systemctl = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "show":
			if !fake.loaded {
				return []byte("unit missing"), errors.New("show failed")
			}
			active := "inactive"
			pid := "0"
			if fake.running {
				active = "active"
				pid = "123"
			}
			return []byte("LoadState=loaded\nActiveState=" + active + "\nMainPID=" + pid + "\n"), nil
		case "list-units":
			if !fake.loaded {
				return nil, nil
			}
			return []byte(m.unit + " loaded active running\n"), nil
		case "stop":
			fake.stopCalls++
			if fake.stopChanges {
				fake.running = false
			}
			return []byte("stop result"), fake.stopErr
		case "disable":
			fake.disableCalls++
			return nil, nil
		case "daemon-reload":
			if _, err := os.Stat(m.path); os.IsNotExist(err) {
				fake.loaded = false
			}
			return nil, nil
		default:
			return nil, errors.New("unexpected systemctl command")
		}
	}
	t.Cleanup(func() { systemctl = original })
}

func TestLinuxStopAcceptsCommandErrorWhenServiceStopped(t *testing.T) {
	m := &platformManager{unit: "yoooclaw-test.service", path: filepath.Join(t.TempDir(), "yoooclaw-test.service")}
	fake := &fakeSystemd{loaded: true, running: true, stopChanges: true, stopErr: errors.New("stop failed")}
	stubSystemctl(t, m, fake)

	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second stop failed: %v", err)
	}
	if fake.stopCalls != 1 {
		t.Fatalf("stop calls = %d", fake.stopCalls)
	}
}

func TestLinuxStopReturnsErrorWhenServiceStillRunning(t *testing.T) {
	m := &platformManager{unit: "yoooclaw-test.service", path: filepath.Join(t.TempDir(), "yoooclaw-test.service")}
	fake := &fakeSystemd{loaded: true, running: true, stopErr: errors.New("stop failed")}
	stubSystemctl(t, m, fake)

	if err := m.Stop(); err == nil {
		t.Fatal("expected stop error while service remains running")
	}
}

func TestLinuxUninstallStopsAndRemovesService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yoooclaw-test.service")
	if err := os.WriteFile(path, []byte("unit"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &platformManager{unit: "yoooclaw-test.service", path: path}
	fake := &fakeSystemd{loaded: true, running: true, stopChanges: true}
	stubSystemctl(t, m, fake)

	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
	if fake.stopCalls != 1 || fake.disableCalls != 1 {
		t.Fatalf("stop calls = %d, disable calls = %d", fake.stopCalls, fake.disableCalls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit still exists or stat failed unexpectedly: %v", err)
	}
}

func TestLinuxBootLinger(t *testing.T) {
	for _, tc := range []struct {
		name       string
		initial    bool
		denied     bool
		unchanged  bool
		wantErr    bool
		wantEnable int
	}{
		{name: "already enabled", initial: true},
		{name: "enable", wantEnable: 1},
		{name: "permission denied", denied: true, wantErr: true, wantEnable: 1},
		{name: "verify failed", unchanged: true, wantErr: true, wantEnable: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := loginctl
			defer func() { loginctl = old }()
			linger := tc.initial
			calls := 0
			loginctl = func(args ...string) ([]byte, error) {
				if args[1] != strconv.Itoa(os.Getuid()) {
					t.Fatalf("wrong user: %v", args)
				}
				switch args[0] {
				case "show-user":
					if linger {
						return []byte("yes\n"), nil
					}
					return []byte("no\n"), nil
				case "enable-linger":
					calls++
					if tc.denied {
						return []byte("Access denied"), errors.New("denied")
					}
					if !tc.unchanged {
						linger = true
					}
					return nil, nil
				}
				t.Fatalf("unexpected loginctl: %v", args)
				return nil, nil
			}
			err := (&platformManager{}).EnableBoot()
			if (err != nil) != tc.wantErr || calls != tc.wantEnable {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestLinuxStatusBootReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, linger, enabled string
		warning               bool
	}{
		{"boot ready", "yes", "enabled", false},
		{"login only", "no", "enabled", true},
		{"disabled unit", "yes", "disabled", true},
		{"unknown linger", "", "enabled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldCtl, oldLogin := systemctl, loginctl
			defer func() { systemctl = oldCtl; loginctl = oldLogin }()
			path := filepath.Join(t.TempDir(), "daemon.service")
			if err := os.WriteFile(path, []byte("unit"), 0600); err != nil {
				t.Fatal(err)
			}
			systemctl = func(args ...string) ([]byte, error) {
				if args[0] == "is-enabled" {
					return []byte(tc.enabled), nil
				}
				return []byte("LoadState=loaded\nActiveState=active\nMainPID=9284"), nil
			}
			loginctl = func(args ...string) ([]byte, error) { return []byte(tc.linger), nil }
			status, err := (&platformManager{unit: "daemon.service", path: path}).Status()
			if err != nil || !status.Running || status.UnitEnabled == nil || *status.UnitEnabled != (tc.enabled == "enabled") || (status.BootWarning != "") != tc.warning {
				t.Fatalf("%+v err=%v", status, err)
			}
			if tc.linger == "" && status.Linger != nil {
				t.Fatal("unknown linger reported as known")
			}
		})
	}
}

func TestLinuxUnitOutputPaths(t *testing.T) {
	log := `/tmp/space dir/100%/daemon "supervisor".log`
	unit := unitText(Spec{Executable: "/bin/true", SupervisorLog: log})
	for _, key := range []string{"StandardOutput=", "StandardError="} {
		if !strings.Contains(unit, key+`append:/tmp/space dir/100%%/daemon "supervisor".log`+"\n") {
			t.Fatalf("incorrect scalar output path: %s", unit)
		}
	}
	// When available, use systemd's actual parser, which returns success even
	// for ignored output directives: inspect diagnostic output as well.
	if tool, err := exec.LookPath("systemd-analyze"); err == nil {
		path := filepath.Join(t.TempDir(), "yoooclaw-test.service")
		if err := os.WriteFile(path, []byte(unit), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(tool, "verify", path).CombinedOutput()
		if err != nil || strings.Contains(string(out), "Failed to parse") || strings.Contains(string(out), "ignoring") {
			t.Fatalf("systemd verify: %v\n%s", err, out)
		}
	}
}

func TestLinuxInstallRejectsUnsafeLogPath(t *testing.T) {
	for _, path := range []string{"relative.log", "/tmp/log\nExecStart=/bin/false", "/tmp/log\r", "/tmp/log\\"} {
		if err := (&platformManager{}).Install(Spec{SupervisorLog: path}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}

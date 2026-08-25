//go:build linux

package autostart

import (
	"errors"
	"os"
	"path/filepath"
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

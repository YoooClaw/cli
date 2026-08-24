package autostart

import (
	"errors"
	"path/filepath"
	"testing"
)

type lifecycleManager struct {
	status         Status
	availableErr   error
	stopCalls      int
	uninstallCalls int
}

func (m *lifecycleManager) Available() error        { return m.availableErr }
func (m *lifecycleManager) Status() (Status, error) { return m.status, nil }
func (m *lifecycleManager) Install(Spec) error      { return nil }
func (m *lifecycleManager) Start() error            { return nil }
func (m *lifecycleManager) Stop() error {
	m.stopCalls++
	m.status.Running = false
	return nil
}
func (m *lifecycleManager) Restart() error { return nil }
func (m *lifecycleManager) Uninstall() error {
	m.uninstallCalls++
	m.status.Installed = false
	m.status.Loaded = false
	m.status.Running = false
	return nil
}

func TestEnableDisablePersistsIntent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	services := filepath.Join(t.TempDir(), "services")
	m := newFileManager(root, services)
	spec := Spec{RootDir: root, Executable: "/tmp/yoooclaw", Arguments: []string{"daemon", "run-service"}}

	status, err := Enable(m, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.Running {
		t.Fatalf("unexpected enabled status: %+v", status)
	}
	if desired, err := Desired(root); err != nil || desired != DesiredEnabled {
		t.Fatalf("desired = %q, err=%v", desired, err)
	}

	status, err = Disable(m, root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed || status.Running {
		t.Fatalf("unexpected disabled status: %+v", status)
	}
	if desired, err := Desired(root); err != nil || desired != DesiredDisabled {
		t.Fatalf("desired = %q, err=%v", desired, err)
	}
}

func TestResolveSpecCarriesExplicitRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "isolated root")
	spec, err := ResolveSpec(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"daemon", "run-service", "--root", filepath.Clean(root)}
	if len(spec.Arguments) != len(want) {
		t.Fatalf("arguments = %#v", spec.Arguments)
	}
	for i := range want {
		if spec.Arguments[i] != want[i] {
			t.Fatalf("arguments = %#v", spec.Arguments)
		}
	}
	if spec.SupervisorLog != filepath.Join(root, "logs", "daemon-supervisor.log") {
		t.Fatalf("supervisor log = %q", spec.SupervisorLog)
	}
}

func TestDisableDelegatesLifecycleToUninstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	m := &lifecycleManager{status: Status{Manager: "fake", Unit: "fake.service", Installed: true, Loaded: true, Running: true}}

	status, err := Disable(m, root)
	if err != nil {
		t.Fatal(err)
	}
	if m.stopCalls != 0 || m.uninstallCalls != 1 {
		t.Fatalf("stop calls = %d, uninstall calls = %d", m.stopCalls, m.uninstallCalls)
	}
	if status.Installed || status.Loaded || status.Running {
		t.Fatalf("unexpected status after disable: %+v", status)
	}
}

func TestDisableUnavailableManagerStillDelegatesToUninstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	m := &lifecycleManager{
		availableErr: errors.New("manager unavailable"),
		status:       Status{Manager: "fake", Unit: "fake.service", Installed: true, Loaded: true, Running: true},
	}

	if _, err := Disable(m, root); err != nil {
		t.Fatal(err)
	}
	if m.stopCalls != 0 || m.uninstallCalls != 1 {
		t.Fatalf("stop calls = %d, uninstall calls = %d", m.stopCalls, m.uninstallCalls)
	}
}

func TestFileManagerStopAndUninstallAreIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	m := newFileManager(root, filepath.Join(t.TempDir(), "services"))
	spec := Spec{RootDir: root, Executable: "/tmp/yoooclaw", Arguments: []string{"daemon", "run-service"}}

	if err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second stop failed: %v", err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
	status, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed || status.Loaded || status.Running {
		t.Fatalf("unexpected status after uninstall: %+v", status)
	}
}

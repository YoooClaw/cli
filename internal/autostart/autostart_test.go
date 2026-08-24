package autostart

import (
	"path/filepath"
	"testing"
)

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

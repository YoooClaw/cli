package autostart

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// fileManager is a deterministic service-manager substitute used by CLI tests.
// It is selected only through YOOOCLAW_AUTOSTART_TEST_DIR.
type fileManager struct {
	root string
	dir  string
	id   string
}

type fileManagerState struct {
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Executable string `json:"executable,omitempty"`
}

func newFileManager(root, dir string) Manager {
	return &fileManager{root: root, dir: dir, id: ServiceID(root)}
}

func (m *fileManager) path() string     { return filepath.Join(m.dir, m.id+".json") }
func (m *fileManager) Available() error { return nil }
func (m *fileManager) read() fileManagerState {
	var state fileManagerState
	raw, err := os.ReadFile(m.path())
	if err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	return state
}
func (m *fileManager) write(state fileManagerState) error {
	return fsutil.WriteJSON(m.path(), state, fsutil.ConfigFileMode)
}
func (m *fileManager) Status() (Status, error) {
	state := m.read()
	return Status{Manager: "test", Unit: m.id, Installed: state.Installed, Loaded: state.Running, Running: state.Running, Executable: state.Executable}, nil
}
func (m *fileManager) Install(spec Spec) error {
	state := m.read()
	state.Installed = true
	state.Executable = spec.Executable
	return m.write(state)
}
func (m *fileManager) Start() error {
	state := m.read()
	state.Installed, state.Running = true, true
	return m.write(state)
}
func (m *fileManager) Stop() error {
	state := m.read()
	state.Running = false
	if !state.Installed {
		return nil
	}
	return m.write(state)
}
func (m *fileManager) Restart() error { return m.Start() }
func (m *fileManager) Uninstall() error {
	if err := m.Stop(); err != nil {
		return err
	}
	if err := os.Remove(m.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

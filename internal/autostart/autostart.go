// Package autostart manages the per-user OS service that supervises the
// standalone daemon. The desired state is account-scoped because only the
// active profile may consume the account Relay at a time.
package autostart

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	DesiredEnabled  = "enabled"
	DesiredDisabled = "disabled"
)

// Spec describes the command installed into the native service manager.
type Spec struct {
	RootDir       string
	Executable    string
	Arguments     []string
	SupervisorLog string
}

// Status is the normalized state returned by every platform manager.
type Status struct {
	Manager    string `json:"manager"`
	Unit       string `json:"unit"`
	Installed  bool   `json:"installed"`
	Loaded     bool   `json:"loaded"`
	Running    bool   `json:"running"`
	PID        int    `json:"pid,omitempty"`
	Executable string `json:"executable,omitempty"`
}

// State records user intent independently of transient OS service state.
type State struct {
	Version    int    `json:"version"`
	Desired    string `json:"desired"`
	Manager    string `json:"manager,omitempty"`
	Unit       string `json:"unit,omitempty"`
	Executable string `json:"executable,omitempty"`
}

// Manager abstracts launchd, systemd --user and Windows Task Scheduler.
// Stop and Uninstall are idempotent. Uninstall must stop the service before
// removing its registration, so callers do not need to coordinate two
// separate lifecycle operations.
type Manager interface {
	Available() error
	Status() (Status, error)
	Install(Spec) error
	Start() error
	Stop() error
	Restart() error
	Uninstall() error
}

// StatePath is intentionally outside profile directories.
func StatePath(root string) string { return filepath.Join(root, "autostart.json") }

func ReadState(root string) (State, bool, error) {
	var state State
	exists, err := fsutil.ReadJSON(StatePath(root), &state)
	return state, exists, err
}

func writeState(root string, state State) error {
	state.Version = 1
	return fsutil.WriteJSON(StatePath(root), state, fsutil.ConfigFileMode)
}

// Enable installs the service, persists intent, and optionally starts it now.
func Enable(m Manager, spec Spec, start bool) (Status, error) {
	if err := m.Available(); err != nil {
		return Status{}, err
	}
	if err := m.Install(spec); err != nil {
		return Status{}, err
	}
	status, err := m.Status()
	if err != nil {
		return Status{}, err
	}
	if err := writeState(spec.RootDir, State{
		Desired: DesiredEnabled, Manager: status.Manager, Unit: status.Unit, Executable: spec.Executable,
	}); err != nil {
		return Status{}, err
	}
	if start {
		if err := m.Start(); err != nil {
			return Status{}, err
		}
		status, err = m.Status()
	}
	return status, err
}

// Disable removes the service, then persists the explicit opt-out.
func Disable(m Manager, root string) (Status, error) {
	if err := m.Available(); err != nil {
		status, _ := m.Status()
		if status.Installed || status.Loaded {
			if uninstallErr := m.Uninstall(); uninstallErr != nil {
				return status, uninstallErr
			}
		}
		if stateErr := writeState(root, State{Desired: DesiredDisabled, Manager: status.Manager, Unit: status.Unit}); stateErr != nil {
			return status, stateErr
		}
		return status, nil
	}
	before, _ := m.Status()
	if err := m.Uninstall(); err != nil {
		return before, err
	}
	if err := writeState(root, State{Desired: DesiredDisabled, Manager: before.Manager, Unit: before.Unit}); err != nil {
		return before, err
	}
	after, err := m.Status()
	return after, err
}

// Desired returns explicit user intent. Missing state is "unknown", allowing
// migration code to distinguish an old install from an explicit opt-out.
func Desired(root string) (string, error) {
	state, exists, err := ReadState(root)
	if err != nil || !exists {
		return "", err
	}
	return state.Desired, nil
}

// RecordDisabled persists an explicit opt-out even when the current platform
// cannot provide a service manager (for example, a container without systemd).
func RecordDisabled(root string) error {
	return writeState(root, State{Desired: DesiredDisabled})
}

// ServiceID uses a stable conventional name for the default root and a short
// hash for explicitly isolated YOOOCLAW_HOME instances.
func ServiceID(root string) string {
	root = filepath.Clean(root)
	defaultRoot := defaultRootDir()
	if samePath(root, defaultRoot) {
		return "yoooclaw-daemon"
	}
	sum := sha256.Sum256([]byte(root))
	return "yoooclaw-daemon-" + hex.EncodeToString(sum[:])[:10]
}

func samePath(a, b string) bool {
	aAbs, _ := filepath.Abs(a)
	bAbs, _ := filepath.Abs(b)
	return filepath.Clean(aAbs) == filepath.Clean(bAbs)
}

func defaultRootDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".yoooclaw")
}

// ResolveSpec captures only the runtime root. Shell/session configuration and
// credentials are deliberately not copied into the service definition.
func ResolveSpec(root string) (Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return Spec{}, err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		RootDir:       filepath.Clean(root),
		Executable:    exe,
		Arguments:     []string{"daemon", "run-service", "--root", filepath.Clean(root)},
		SupervisorLog: filepath.Join(root, "logs", "daemon-supervisor.log"),
	}, nil
}

func Current(root string) Manager {
	if dir := strings.TrimSpace(os.Getenv("YOOOCLAW_AUTOSTART_TEST_DIR")); dir != "" {
		return newFileManager(root, dir)
	}
	return newPlatformManager(root)
}

var ErrUnavailable = errors.New("当前环境没有可用的用户级服务管理器")

const (
	serviceStatePollInterval = 50 * time.Millisecond
	serviceStatePollTimeout  = 3 * time.Second
)

// waitForServiceState allows native service managers to converge after a
// synchronous lifecycle command returns. launchd in particular can report a
// service as loaded briefly after bootout has already accepted the request.
func waitForServiceState(status func() (Status, error), done func(Status) bool) (Status, error) {
	return waitForServiceStateWithin(status, done, serviceStatePollTimeout)
}

func waitForServiceStateWithin(status func() (Status, error), done func(Status) bool, timeout time.Duration) (Status, error) {
	deadline := time.Now().Add(timeout)
	var current Status
	var err error
	for {
		current, err = status()
		if err == nil && done(current) {
			return current, nil
		}
		if !time.Now().Before(deadline) {
			return current, err
		}
		time.Sleep(serviceStatePollInterval)
	}
}

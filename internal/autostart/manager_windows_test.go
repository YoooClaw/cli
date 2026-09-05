//go:build windows

package autostart

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeTaskScheduler struct {
	installed    bool
	running      bool
	availableErr error
	stopChanges  bool
	stopErr      error
	stopCalls    int
	deleteCalls  int
	installCalls int
	installXML   string
}

func stubTaskSchedulerCOM(t *testing.T, fake *fakeTaskScheduler) {
	t.Helper()
	original := taskSchedulerCOM
	taskSchedulerCOM = func(action string, args ...string) ([]byte, error) {
		switch action {
		case "available":
			if fake.availableErr != nil {
				return []byte("blocked by policy"), fake.availableErr
			}
			return []byte("ok"), nil
		case "identity":
			return []byte("S-1-5-21-test"), nil
		case "status":
			if !fake.installed {
				return []byte("missing"), nil
			}
			if fake.running {
				return []byte("4"), nil
			}
			return []byte("3"), nil
		case "install":
			if len(args) != 3 {
				return nil, errors.New("invalid install args")
			}
			raw, err := os.ReadFile(args[2])
			if err != nil {
				return nil, err
			}
			fake.installCalls++
			fake.installXML = strings.ReplaceAll(string(raw), "\x00", "")
			fake.installed = true
			return []byte("ok"), nil
		case "start":
			fake.installed, fake.running = true, true
			return []byte("ok"), nil
		case "stop":
			fake.stopCalls++
			if fake.stopChanges {
				fake.running = false
			}
			return []byte("stop result"), fake.stopErr
		case "delete":
			fake.deleteCalls++
			fake.installed, fake.running = false, false
			return []byte("ok"), nil
		default:
			return nil, errors.New("unexpected Task Scheduler COM operation")
		}
	}
	t.Cleanup(func() { taskSchedulerCOM = original })
}

func newWindowsTestManager(t *testing.T) *platformManager {
	t.Helper()
	t.Setenv("SystemRoot", `C:\Windows`)
	return &platformManager{task: `\YoooClaw\yoooclaw-test`}
}

func TestWindowsAvailableUsesTaskSchedulerCOM(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{availableErr: errors.New("access denied")}
	stubTaskSchedulerCOM(t, fake)

	if err := m.Available(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Available error = %v, want ErrUnavailable", err)
	}
}

func TestWindowsStatusUsesLanguageIndependentNumericState(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true}
	stubTaskSchedulerCOM(t, fake)

	status, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Running {
		t.Fatalf("running task status = %+v", status)
	}
}

func TestWindowsTaskUsesHiddenPowerShellHost(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{}
	stubTaskSchedulerCOM(t, fake)
	spec := Spec{
		RootDir:    `C:\Users\O'Brien\.yoooclaw`,
		Executable: `C:\Program Files\YoooClaw\yoooclaw.exe`,
		Arguments:  []string{"daemon", "run-service", "--root", `C:\Users\O'Brien\.yoooclaw`},
	}
	if err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	if fake.installCalls != 1 {
		t.Fatalf("install calls = %d", fake.installCalls)
	}
	for _, want := range []string{
		`<Command>C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe</Command>`,
		`-WindowStyle Hidden`,
		`<Hidden>true</Hidden>`,
		`&amp;`,
		`O&#39;&#39;Brien`,
		`yoooclaw.exe`,
	} {
		if !strings.Contains(fake.installXML, want) {
			t.Fatalf("task XML does not contain %q", want)
		}
	}
	if strings.Contains(fake.installXML, `<Command>`+spec.Executable+`</Command>`) {
		t.Fatal("console executable is still registered as the visible task host")
	}
}

func TestWindowsStopAcceptsCommandErrorWhenTaskStopped(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true, stopChanges: true, stopErr: errors.New("stop failed")}
	stubTaskSchedulerCOM(t, fake)

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

func TestWindowsStopReturnsErrorWhenTaskStillRunning(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true, stopErr: errors.New("stop failed")}
	stubTaskSchedulerCOM(t, fake)
	originalTimeout := windowsTaskStopTimeout
	windowsTaskStopTimeout = 20 * time.Millisecond
	t.Cleanup(func() { windowsTaskStopTimeout = originalTimeout })

	if err := m.Stop(); err == nil {
		t.Fatal("expected stop error while task remains running")
	}
}

func TestWindowsUninstallStopsAndDeletesTask(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true, stopChanges: true}
	stubTaskSchedulerCOM(t, fake)

	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
	if fake.stopCalls != 1 || fake.deleteCalls != 1 {
		t.Fatalf("stop calls = %d, delete calls = %d", fake.stopCalls, fake.deleteCalls)
	}
}

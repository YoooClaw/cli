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
	statusErr    error
	installErr   error
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
			if fake.statusErr != nil {
				return nil, fake.statusErr
			}
			if !fake.installed {
				return []byte("missing"), nil
			}
			if fake.running {
				return []byte("4"), nil
			}
			return []byte("3"), nil
		case "install":
			if len(args) != 4 {
				return nil, errors.New("invalid install args")
			}
			fake.installCalls++
			fake.installXML = args[2]
			if fake.installErr != nil {
				return nil, fake.installErr
			}
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
	return &platformManager{root: t.TempDir(), task: `\YoooClaw\yoooclaw-test`}
}

func TestWindowsAvailableUsesTaskSchedulerCOM(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{availableErr: errors.New("access denied")}
	stubTaskSchedulerCOM(t, fake)

	if err := m.Available(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Available error = %v, want ErrUnavailable", err)
	}
}

func TestWindowsStatusPreservesAccessDenied(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{statusErr: &taskSchedulerError{
		operation: "GetTask", hresult: 0x80070005, cause: errors.New("access denied"),
	}}
	stubTaskSchedulerCOM(t, fake)
	status, err := m.Status()
	if err == nil || !strings.Contains(err.Error(), "GetTask") || !strings.Contains(err.Error(), "0x80070005") {
		t.Fatalf("status = %+v, err = %v; want original operation and HRESULT", status, err)
	}
}

func TestWindowsInstallPreservesNativeFailureDetails(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installErr: &taskSchedulerError{
		operation: "RegisterTask", hresult: 0x80070005, cause: errors.New("access denied"),
	}}
	stubTaskSchedulerCOM(t, fake)
	err := m.Install(Spec{RootDir: m.root, Executable: `C:\YoooClaw\yoooclaw.exe`})
	if err == nil || !strings.Contains(err.Error(), "RegisterTask") || !strings.Contains(err.Error(), "0x80070005") {
		t.Fatalf("install error lost native details: %v", err)
	}
	if fake.installed {
		t.Fatal("failed registration was reported as installed")
	}
}

func TestWindowsUninstallDoesNotDeleteLauncherWhenTaskAccessDenied(t *testing.T) {
	m := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{statusErr: &taskSchedulerError{
		operation: "GetTask", hresult: 0x80070005, cause: errors.New("access denied"),
	}}
	stubTaskSchedulerCOM(t, fake)
	if err := os.WriteFile(m.launcherPath(), []byte("keep launcher"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err == nil {
		t.Fatal("uninstall reported success despite denied task access")
	}
	if _, err := os.Stat(m.launcherPath()); err != nil || fake.deleteCalls != 0 {
		t.Fatalf("uninstall removed assets without confirming task state: %v", err)
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

func TestWindowsTaskUsesHiddenWScriptHost(t *testing.T) {
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
		`<LogonTrigger><UserId>S-1-5-21-test</UserId>`,
		`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`<StartWhenAvailable>true</StartWhenAvailable>`,
		`<Command>C:\Windows\System32\wscript.exe</Command>`,
		`//B //NoLogo`,
		`yoooclaw-daemon-hidden.vbs`,
		`<Hidden>true</Hidden>`,
	} {
		if !strings.Contains(fake.installXML, want) {
			t.Fatalf("task XML does not contain %q", want)
		}
	}
	if strings.Contains(fake.installXML, `<Command>`+spec.Executable+`</Command>`) {
		t.Fatal("console executable is still registered as the visible task host")
	}
	if strings.Contains(fake.installXML, `powershell.exe`) {
		t.Fatal("PowerShell is still registered as the task host")
	}
	launcher, err := os.ReadFile(m.launcherPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`CreateObject("WScript.Shell")`,
		`shell.Run(`,
		`, 0, True)`,
		`"C:\Program Files\YoooClaw\yoooclaw.exe"`,
		`C:\Users\O'Brien\.yoooclaw`,
		`--format json`,
	} {
		if !strings.Contains(string(launcher), want) {
			t.Fatalf("hidden launcher does not contain %q:\n%s", want, launcher)
		}
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
	if err := os.WriteFile(m.launcherPath(), []byte("launcher"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
	if fake.stopCalls != 1 || fake.deleteCalls != 1 {
		t.Fatalf("stop calls = %d, delete calls = %d", fake.stopCalls, fake.deleteCalls)
	}
	if _, err := os.Stat(m.launcherPath()); !os.IsNotExist(err) {
		t.Fatalf("hidden launcher still exists or stat failed unexpectedly: %v", err)
	}
}

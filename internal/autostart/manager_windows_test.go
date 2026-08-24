//go:build windows

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeTaskScheduler struct {
	installed    bool
	running      bool
	endChanges   bool
	endErr       error
	endCalls     int
	deleteCalls  int
	taskFilePath string
}

func stubSchtasks(t *testing.T, fake *fakeTaskScheduler) {
	t.Helper()
	original := schtasks
	schtasks = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "/Query":
			if !fake.installed {
				return []byte("task missing"), errors.New("query failed")
			}
			status := "Ready"
			if fake.running {
				status = "Running"
			}
			return []byte("Status: " + status), nil
		case "/End":
			fake.endCalls++
			if fake.endChanges {
				fake.running = false
			}
			return []byte("end result"), fake.endErr
		case "/Delete":
			fake.deleteCalls++
			fake.installed = false
			_ = os.Remove(fake.taskFilePath)
			return nil, nil
		default:
			return nil, errors.New("unexpected schtasks command")
		}
	}
	t.Cleanup(func() { schtasks = original })
}

func newWindowsTestManager(t *testing.T) (*platformManager, string) {
	t.Helper()
	systemRoot := t.TempDir()
	t.Setenv("SystemRoot", systemRoot)
	m := &platformManager{task: `\YoooClaw\yoooclaw-test`}
	taskFile := filepath.Join(systemRoot, "System32", "Tasks", "YoooClaw", "yoooclaw-test")
	if err := os.MkdirAll(filepath.Dir(taskFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskFile, []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, taskFile
}

func TestWindowsStopAcceptsCommandErrorWhenTaskStopped(t *testing.T) {
	m, taskFile := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true, endChanges: true, endErr: errors.New("end failed"), taskFilePath: taskFile}
	stubSchtasks(t, fake)

	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second stop failed: %v", err)
	}
	if fake.endCalls != 1 {
		t.Fatalf("end calls = %d", fake.endCalls)
	}
}

func TestWindowsStopReturnsErrorWhenTaskStillRunning(t *testing.T) {
	m, taskFile := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true, endErr: errors.New("end failed"), taskFilePath: taskFile}
	stubSchtasks(t, fake)

	if err := m.Stop(); err == nil {
		t.Fatal("expected stop error while task remains running")
	}
}

func TestWindowsUninstallStopsAndDeletesTask(t *testing.T) {
	m, taskFile := newWindowsTestManager(t)
	fake := &fakeTaskScheduler{installed: true, running: true, endChanges: true, taskFilePath: taskFile}
	stubSchtasks(t, fake)

	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
	if fake.endCalls != 1 || fake.deleteCalls != 1 {
		t.Fatalf("end calls = %d, delete calls = %d", fake.endCalls, fake.deleteCalls)
	}
	if _, err := os.Stat(taskFile); !os.IsNotExist(err) {
		t.Fatalf("task file still exists or stat failed unexpectedly: %v", err)
	}
}

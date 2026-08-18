package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestActivateCLIOwnerDisablesHermesAndLeavesUnconfiguredInstallPending(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("YOOOCLAW_HOME", root)

	oldFind, oldRun := findExecutable, runExecutable
	t.Cleanup(func() { findExecutable, runExecutable = oldFind, oldRun })
	findExecutable = func(name string) (string, error) {
		if name != "hermes" {
			t.Fatalf("unexpected executable lookup: %s", name)
		}
		return "/mock/hermes", nil
	}
	var calls [][]string
	runExecutable = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte("ok"), nil
	}

	ctx := &clictx.Context{Profile: "default", Paths: paths.For("default")}
	result, err := activateCLIOwner(ctx, "work", true)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{
		{"/mock/hermes", "--profile", "work", "plugins", "disable", "yoooclaw"},
		{"/mock/hermes", "--profile", "work", "plugins", "disable", "yoooclaw_app"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("Hermes calls = %#v, want %#v", calls, wantCalls)
	}
	if result["owner"] != "standalone-daemon" || result["activation"] != "pending-config" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result["gatewayRestarted"] != false || result["daemonStarted"] != false {
		t.Fatalf("unheld/unconfigured install should not restart/start: %#v", result)
	}
}

func TestFindHermesExecutableFallsBackToKnownUserPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", "")
	oldFind := findExecutable
	t.Cleanup(func() { findExecutable = oldFind })
	findExecutable = func(string) (string, error) { return "", errors.New("not on PATH") }

	want := filepath.Join(home, ".local", "bin", "hermes")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findHermesExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("findHermesExecutable() = %q, want %q", got, want)
	}
}

func TestHermesArgsUsesEnvironmentProfile(t *testing.T) {
	t.Setenv("HERMES_PROFILE", "customer")
	got := hermesArgs("", "gateway", "restart")
	want := []string{"--profile", "customer", "gateway", "restart"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hermesArgs = %#v, want %#v", got, want)
	}
}

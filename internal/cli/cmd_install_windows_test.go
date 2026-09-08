//go:build windows

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A real native installation/uninstallation, not an installer mock. The parent
// restores its user PATH even on failure; the test uses only its isolated root.
func TestWindowsNativeInstallerEndToEnd(t *testing.T) {
	source := os.Getenv("YOOOCLAW_NATIVE_INSTALLER_TEST_BINARY")
	if source == "" {
		t.Skip("set YOOOCLAW_NATIVE_INSTALLER_TEST_BINARY to a native setup build")
	}
	before, err := readWindowsUserPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writeWindowsUserPath(before); err != nil {
			t.Errorf("restore PATH: %v", err)
		}
	})
	root := t.TempDir()
	appData := filepath.Join(root, "用户 O'Brien & local")
	runtimeRoot := filepath.Join(root, "runtime")
	download := filepath.Join(root, "YoooClaw 安装 (1).exe")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(download, data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", appData)
	t.Setenv("YOOOCLAW_HOME", runtimeRoot)
	t.Setenv("YOOOCLAW_PROFILE", "")
	t.Setenv("YOOOCLAW_AUTOSTART_TEST_DIR", "")
	t.Setenv("PATH", t.TempDir()) // No script hosts or external tools can be found.
	run := func(exe string, args ...string) map[string]any {
		t.Helper()
		command := exec.Command(exe, args...)
		out, err := command.Output()
		if err != nil {
			t.Fatalf("native command %v failed: %v", args, err)
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal(err)
		}
		if result["ok"] != true {
			t.Fatalf("native command %v returned failure", args)
		}
		return result
	}
	installed := run(download, "--yes", "--force", "--format", "json")
	exe := filepath.Join(appData, "YoooClaw", "bin", "yoooclaw.exe")
	if installed["executable"] != exe || installed["installed"] != true {
		t.Fatalf("install result=%v", installed)
	}
	state, err := readWindowsUserPath()
	if err != nil || !strings.Contains(state.Value, filepath.Dir(exe)) {
		t.Fatal("native install did not configure PATH")
	}
	if err := exec.Command(download, "--yes", "--format", "json").Run(); err == nil {
		t.Fatal("overwrite without --force succeeded")
	}
	run(exe, "config", "init", "--defaults", "--no-start", "--no-autostart", "--format", "json")
	configPath := filepath.Join(runtimeRoot, "profiles", "default", "config.json")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(runtimeRoot, "profiles", "default", "notifications", "keep.json")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(download, "--yes", "--force", "--format", "json")
	current, _ := os.ReadFile(configPath)
	if string(current) != string(original) {
		t.Fatal("upgrade changed existing config")
	}
	if b, _ := os.ReadFile(dataPath); string(b) != "preserve" {
		t.Fatal("upgrade lost data")
	}
	result := run(exe, "uninstall", "--yes", "--data", "--format", "json")
	if result["userPathRemoved"] != true {
		t.Fatal("uninstall did not verify PATH cleanup")
	}
	for _, name := range []string{"yoooclaw.exe", "yc.exe"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), name)); !os.IsNotExist(err) {
			t.Fatalf("uninstall left %s: %v", name, err)
		}
	}
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatal("test runtime data remained after explicit --data")
	}
}

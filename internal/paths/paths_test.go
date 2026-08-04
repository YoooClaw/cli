package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootDirEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	if RootDir() != home {
		t.Errorf("RootDir = %q, want %q", RootDir(), home)
	}
}

func TestRootDirEnvTrimmed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", "  "+home+"  ")
	if RootDir() != home {
		t.Errorf("RootDir should trim whitespace, got %q", RootDir())
	}
}

func TestRootDirDefault(t *testing.T) {
	t.Setenv("YOOOCLAW_HOME", "")
	uh, _ := os.UserHomeDir()
	want := filepath.Join(uh, ".yoooclaw")
	if RootDir() != want {
		t.Errorf("RootDir default = %q, want %q", RootDir(), want)
	}
}

func TestDerivedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	if SharedCredentialsPath() != filepath.Join(home, "credentials.json") {
		t.Errorf("SharedCredentialsPath = %q", SharedCredentialsPath())
	}
	if ActiveProfilePath() != filepath.Join(home, "active-profile") {
		t.Errorf("ActiveProfilePath = %q", ActiveProfilePath())
	}
	if ProfilesRoot() != filepath.Join(home, "profiles") {
		t.Errorf("ProfilesRoot = %q", ProfilesRoot())
	}
	if ProfileDir("p1") != filepath.Join(home, "profiles", "p1") {
		t.Errorf("ProfileDir = %q", ProfileDir("p1"))
	}
}

func TestFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	p := For("acme")
	base := filepath.Join(home, "profiles", "acme")
	if p.Profile != "acme" || p.Dir != base {
		t.Fatalf("For base mismatch: %+v", p)
	}
	cases := map[string]string{
		p.Config: "config.json", p.Credentials: "credentials.json",
		p.DaemonLock: "daemon.lock", p.Logs: "logs",
		p.DaemonLog:     filepath.Join("logs", "daemon.log"),
		p.Notifications: "notifications", p.Recordings: "recordings",
		p.Voice:  "voice",
		p.Images: "images", p.WebPages: "web-pages",
		p.LightRules: "light-rules", p.State: "state",
	}
	for got, leaf := range cases {
		if got != filepath.Join(base, leaf) {
			t.Errorf("path for %q = %q", leaf, got)
		}
	}
}

func TestMigrateLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	p := For("acme")
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// 旧布局：日志直接放在 profile 根目录。
	old := []string{"daemon.log", "daemon.log.2026-06-01", "daemon.log.2026-06-02"}
	for _, name := range old {
		if err := os.WriteFile(filepath.Join(p.Dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 非日志文件不应被搬动。
	if err := os.WriteFile(p.Config, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	p.MigrateLogs()

	for _, name := range old {
		if _, err := os.Stat(filepath.Join(p.Dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s 仍在根目录，应已搬走", name)
		}
		if _, err := os.Stat(filepath.Join(p.Logs, name)); err != nil {
			t.Errorf("%s 未出现在 logs/: %v", name, err)
		}
	}
	if _, err := os.Stat(p.Config); err != nil {
		t.Errorf("config.json 不应被搬动: %v", err)
	}

	// 已是新布局时为幂等 no-op，不报错。
	p.MigrateLogs()
}

func TestListProfileNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	if got := ListProfileNames(); got != nil {
		t.Errorf("no profiles dir -> nil, got %v", got)
	}
	for _, name := range []string{"zeta", "alpha", "beta"} {
		if err := os.MkdirAll(ProfileDir(name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// 放一个文件，确认只统计目录。
	if err := os.WriteFile(filepath.Join(ProfilesRoot(), "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ListProfileNames()
	want := []string{"alpha", "beta", "zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ListProfileNames = %v, want sorted %v", got, want)
	}
}

func TestReadActiveProfileRejectsMissingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	if err := os.WriteFile(ActiveProfilePath(), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadActiveProfile(); got != "" {
		t.Fatalf("missing profile directory should be ignored, got %q", got)
	}
	if err := os.MkdirAll(ProfileDir("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := ReadActiveProfile(); got != "test" {
		t.Fatalf("existing active profile = %q, want test", got)
	}
}

func TestReadActiveProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	if got := ReadActiveProfile(); got != "" {
		t.Errorf("missing file -> empty, got %q", got)
	}
	if err := os.WriteFile(ActiveProfilePath(), []byte("  work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ProfileDir("work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := ReadActiveProfile(); got != "work" {
		t.Errorf("ReadActiveProfile = %q, want work", got)
	}
}

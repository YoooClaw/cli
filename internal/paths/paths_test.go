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
		p.DaemonLock: "daemon.lock", p.DaemonLog: "daemon.log",
		p.Notifications: "notifications", p.Recordings: "recordings",
		p.Images: "images", p.LightRules: "light-rules", p.State: "state",
	}
	for got, leaf := range cases {
		if got != filepath.Join(base, leaf) {
			t.Errorf("path for %q = %q", leaf, got)
		}
	}
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

func TestReadActiveProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	if got := ReadActiveProfile(); got != "" {
		t.Errorf("missing file -> empty, got %q", got)
	}
	if err := os.WriteFile(ActiveProfilePath(), []byte("  work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadActiveProfile(); got != "work" {
		t.Errorf("ReadActiveProfile = %q, want work", got)
	}
}

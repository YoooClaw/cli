package clictx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/paths"
)

func TestResolveActiveProfileFlagWins(t *testing.T) {
	t.Setenv("YOOOCLAW_PROFILE", "envp")
	if got := ResolveActiveProfile("  flagp  "); got != "flagp" {
		t.Errorf("flag should win: %q", got)
	}
}

func TestResolveActiveProfileEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	t.Setenv("YOOOCLAW_PROFILE", "envp")
	if got := ResolveActiveProfile(""); got != "envp" {
		t.Errorf("env should win over file/default: %q", got)
	}
}

func TestResolveActiveProfileFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	t.Setenv("YOOOCLAW_PROFILE", "")
	if err := os.MkdirAll(paths.ProfileDir("filep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ActiveProfilePath(), []byte("filep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveActiveProfile(""); got != "filep" {
		t.Errorf("file profile should be used: %q", got)
	}
}

func TestResolveActiveProfileMissingDirectoryFallsBackToDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	t.Setenv("YOOOCLAW_PROFILE", "")
	if err := os.WriteFile(paths.ActiveProfilePath(), []byte("deleted-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveActiveProfile(""); got != paths.DefaultProfile {
		t.Errorf("deleted active profile should fall back to default: %q", got)
	}
}

func TestResolveActiveProfileDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	t.Setenv("YOOOCLAW_PROFILE", "")
	if got := ResolveActiveProfile(""); got != paths.DefaultProfile {
		t.Errorf("should fall back to default: %q", got)
	}
}

func TestBuild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	t.Setenv("YOOOCLAW_PROFILE", "")
	ctx, err := Build("work", "json", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Profile != "work" {
		t.Errorf("profile = %q", ctx.Profile)
	}
	if ctx.Paths.Dir != paths.ProfileDir("work") {
		t.Errorf("paths not wired: %q", ctx.Paths.Dir)
	}
	if string(ctx.Format) != "json" || !ctx.Quiet || ctx.Color {
		t.Errorf("flags not materialized: %+v", ctx)
	}
	_ = filepath.Separator
}

func TestBuildInvalidFormat(t *testing.T) {
	t.Setenv("YOOOCLAW_HOME", t.TempDir())
	if _, err := Build("", "yaml", false, false); err == nil {
		t.Error("invalid format should error")
	}
}

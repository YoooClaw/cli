package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/paths"
)

// seedProfile 在沙箱里造出一个带配置 + 数据的 profile。
func seedProfile(t *testing.T) (cfg, cred, data string) {
	t.Helper()
	p := paths.For(paths.DefaultProfile)
	if err := os.MkdirAll(p.Notifications, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cfg = write(p.Config, "{}")
	cred = write(p.Credentials, "{}")
	write(paths.SharedCredentialsPath(), "{}")
	write(paths.ActiveProfilePath(), paths.DefaultProfile)
	data = write(filepath.Join(p.Notifications, "n1.json"), "x")
	return cfg, cred, data
}

func TestUninstallDefaultRemovesConfigKeepsData(t *testing.T) {
	sandbox(t)
	cfg, cred, data := seedProfile(t)

	out, code := execCLI(t, "uninstall", "--yes", "--format", "json")
	m := decode(t, out)
	if code != 0 || m["ok"] != true {
		t.Fatalf("expected ok exit, got code=%d out=%s", code, out)
	}
	if m["dataKept"] != true {
		t.Errorf("expected dataKept=true, got %v", m["dataKept"])
	}
	for _, f := range []string{cfg, cred, paths.SharedCredentialsPath(), paths.ActiveProfilePath()} {
		if _, err := os.Lstat(f); err == nil {
			t.Errorf("config file should be removed: %s", f)
		}
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("data file should be kept: %s (%v)", data, err)
	}
}

func TestUninstallWithDataRemovesEverything(t *testing.T) {
	home := sandbox(t)
	seedProfile(t)

	out, code := execCLI(t, "uninstall", "--data", "--yes", "--format", "json")
	m := decode(t, out)
	if code != 0 || m["ok"] != true {
		t.Fatalf("expected ok exit, got code=%d out=%s", code, out)
	}
	if m["dataKept"] != false {
		t.Errorf("expected dataKept=false, got %v", m["dataKept"])
	}
	if _, err := os.Stat(paths.RootDir()); !os.IsNotExist(err) {
		t.Errorf("root dir should be gone: %s (%v)", home, err)
	}
}

func TestUninstallNonInteractiveWithoutYesCancels(t *testing.T) {
	sandbox(t)
	cfg, _, _ := seedProfile(t)

	out, code := execCLI(t, "uninstall", "--format", "json")
	if code == 0 {
		t.Fatalf("expected non-zero exit without --yes in non-interactive, out=%s", out)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Errorf("config must be preserved when cancelled: %v", err)
	}
}

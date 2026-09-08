package installer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallTransactionRollbackAndCommit(t *testing.T) {
	for _, failure := range []string{"", "stop", "verify", "path"} {
		t.Run(failure, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "用户 O'Brien & bin")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "setup.exe")
			if err := os.WriteFile(source, []byte("new"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"yoooclaw.exe", "yc.exe", "unrelated.txt"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			stopped, resumed, pathRestored := false, false, false
			h := Hooks{
				Stop: func() error {
					stopped = true
					if failure == "stop" {
						return fmt.Errorf("stop denied")
					}
					return nil
				},
				Verify: func(path string) error {
					if !stopped {
						t.Fatal("verify before stop")
					}
					if failure == "verify" {
						return fmt.Errorf("bad version")
					}
					return nil
				},
				CommitPath: func() error {
					if failure == "path" {
						return fmt.Errorf("PATH denied")
					}
					return nil
				},
				RestorePath: func() error { pathRestored = true; return nil }, Resume: func() error { resumed = true; return nil },
			}
			result, err := Install(source, dir, true, h)
			if (err != nil) != (failure != "") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			want := "new"
			if failure != "" {
				want = "old"
				if !resumed || !pathRestored {
					t.Fatal("rollback omitted lifecycle or PATH")
				}
			}
			for _, name := range []string{"yoooclaw.exe", "yc.exe"} {
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(b) != want {
					t.Fatalf("%s=%q err=%v", name, b, err)
				}
			}
			b, _ := os.ReadFile(filepath.Join(dir, "unrelated.txt"))
			if string(b) != "old" {
				t.Fatal("unrelated file changed")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 3 {
				t.Fatalf("transaction files leaked: %v", entries)
			}
		})
	}
}

func TestInstallDoesNotStopBeforePreflight(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.exe")
	_ = os.WriteFile(source, []byte("new"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "yoooclaw.exe"), []byte("old"), 0o700)
	_, err := Install(source, dir, false, Hooks{Stop: func() error { t.Fatal("stopped despite missing --force"); return nil }})
	if err == nil {
		t.Fatal("expected overwrite rejection")
	}
}

func TestDownloadSetupRequiresMatchingChecksum(t *testing.T) {
	for _, mode := range []string{"ok", "missing", "bad", "duplicate", "http-error"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("native setup")
			sum := sha256.Sum256(body)
			manifest := fmt.Sprintf("%x  %s\n", sum, SetupAsset)
			switch mode {
			case "missing":
				manifest = ""
			case "bad":
				manifest = strings.Repeat("0", 64) + "  " + SetupAsset
			case "duplicate":
				manifest += manifest
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "http-error" {
					http.Error(w, "no", 503)
					return
				}
				if r.URL.Path == "/v1.2.3/checksums.txt" {
					fmt.Fprint(w, manifest)
					return
				}
				if r.URL.Path == "/v1.2.3/"+SetupAsset {
					_, _ = w.Write(body)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			dir := t.TempDir()
			path, err := DownloadSetup(context.Background(), server.Client(), server.URL, "1.2.3", dir)
			if mode == "ok" {
				b, e := os.ReadFile(path)
				if err != nil || e != nil || string(b) != string(body) {
					t.Fatalf("download=%q %v %v", b, err, e)
				}
			} else {
				if err == nil {
					t.Fatal("unchecked setup accepted")
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatal("invalid setup left executable")
				}
			}
		})
	}
}

func TestInvalidDownloadVersions(t *testing.T) {
	for _, v := range []string{"../latest", "1.2.3 & calc", "1.2.3/../../x", ""} {
		if ValidVersion(v) {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, v := range []string{"1.2.3", "0.10.1-beta.2"} {
		if !ValidVersion(v) {
			t.Fatalf("rejected %q", v)
		}
	}
}

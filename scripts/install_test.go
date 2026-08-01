package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptResolvesGitHubVersionPortably(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		args             []string
		wantVersion      string
		wantPathModified bool
	}{
		{name: "stable", wantVersion: "0.7.2"},
		{name: "prerelease with legacy opt-out", args: []string{"--beta", "--no-modify-path"}, wantVersion: "0.8.0-beta.1"},
		{name: "explicit path opt-in", args: []string{"--modify-path"}, wantVersion: "0.7.2", wantPathModified: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			mockBin := filepath.Join(root, "mock-bin")
			installDir := filepath.Join(root, "install-bin")
			curlLog := filepath.Join(root, "curl.log")
			mustMkdirAll(t, mockBin)

			writeExecutable(t, filepath.Join(mockBin, "uname"), `#!/bin/sh
case "$1" in
  -s) printf 'Darwin\n' ;;
  -m) printf 'arm64\n' ;;
  *) exit 2 ;;
esac
`)
			// This shim makes the original BSD-incompatible \s expression fail on
			// every platform, while delegating portable sed calls to the system sed.
			writeExecutable(t, filepath.Join(mockBin, "sed"), `#!/bin/sh
case "$*" in
  *'\s'*) exit 64 ;;
esac
exec /usr/bin/sed "$@"
`)
			writeExecutable(t, filepath.Join(mockBin, "curl"), `#!/bin/sh
printf '%s\n' "$*" >> "$CURL_LOG"
case "$*" in
  *api.github.com*)
    printf '%s\n' '[{"tag_name" : "cli-v0.8.0-beta.1"},{"tag_name": "cli-v0.7.2"}]'
    ;;
  *checksums.txt*)
    exit 22
    ;;
  *)
    destination=''
    while [ "$#" -gt 0 ]; do
      if [ "$1" = '-o' ]; then
        destination=$2
        shift 2
      else
        shift
      fi
    done
    [ -n "$destination" ] || exit 2
    printf '%s\n' '#!/bin/sh' 'printf "0.7.2\\n"' > "$destination"
    ;;
esac
`)

			args := []string{mustAbs(t, "install.sh")}
			args = append(args, tc.args...)
			args = append(args, "--dir", installDir)
			cmd := exec.Command("sh", args...)
			cmd.Env = append(os.Environ(),
				"HOME="+root,
				"SHELL=/bin/zsh",
				"CURL_LOG="+curlLog,
				"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install.sh failed: %v\n%s", err, output)
			}

			logBytes, err := os.ReadFile(curlLog)
			if err != nil {
				t.Fatal(err)
			}
			logText := string(logBytes)
			wantURL := "/releases/download/cli-v" + tc.wantVersion + "/yoooclaw-darwin-arm64"
			if !strings.Contains(logText, wantURL) {
				t.Fatalf("download URL does not contain %q:\n%s", wantURL, logText)
			}
			if strings.Contains(logText, "tag_name") {
				t.Fatalf("raw JSON leaked into download URL:\n%s", logText)
			}
			zshrc, err := os.ReadFile(filepath.Join(root, ".zshrc"))
			if tc.wantPathModified {
				if err != nil {
					t.Fatalf("--modify-path did not create .zshrc: %v", err)
				}
				if !strings.Contains(string(zshrc), `$HOME/install-bin`) {
					t.Fatalf("--modify-path did not add install directory:\n%s", zshrc)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("default/--no-modify-path unexpectedly touched .zshrc: %v", err)
			}
		})
	}
}

func TestInstallScriptRejectsInvalidExplicitVersion(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", mustAbs(t, "install.sh"), "--version", `{"tag_name":"cli-v0.7.2"}`, "--no-modify-path")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install.sh unexpectedly accepted invalid version:\n%s", output)
	}
	if !strings.Contains(string(output), "无效版本号") {
		t.Fatalf("unexpected error:\n%s", output)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

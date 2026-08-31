package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		wantActivated    bool
	}{
		{name: "stable", wantVersion: "0.7.2"},
		{name: "prerelease with legacy opt-out", args: []string{"--beta", "--no-modify-path"}, wantVersion: "0.8.0-beta.1"},
		{name: "explicit path opt-in", args: []string{"--modify-path"}, wantVersion: "0.7.2", wantPathModified: true},
		{name: "explicit owner activation", args: []string{"--activate"}, wantVersion: "0.7.2", wantActivated: true},
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
			activated := strings.Contains(string(output), "切换 Relay owner 到 standalone CLI")
			if activated != tc.wantActivated {
				t.Fatalf("owner activation = %v, want %v:\n%s", activated, tc.wantActivated, output)
			}
			if !tc.wantActivated && !strings.Contains(string(output), "已保留当前 Relay owner") {
				t.Fatalf("installer did not preserve owner by default:\n%s", output)
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

func TestInstallScriptRequiresExplicitOwnerActivation(t *testing.T) {
	t.Parallel()
	text, err := os.ReadFile(mustAbs(t, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(text)
	for _, want := range []string{"--activate", "YOOOCLAW_ACTIVATE_OWNER", "stop_existing_daemons", "yoooclaw owner activate cli", "daemon autostart migrate", "已保留当前 Relay owner"} {
		if !strings.Contains(script, want) {
			t.Fatalf("install.sh missing %q", want)
		}
	}
}

func TestWindowsInstallScriptIsNativeAndSelfContained(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(mustAbs(t, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(text)
	for _, want := range []string{
		"yoooclaw-win32-x64.exe",
		"Get-FileHash -Algorithm SHA256",
		"YoooClaw\\bin",
		"yoooclaw.exe",
		"yc.exe",
		"SetEnvironmentVariable(\"Path\"",
		"Stop-ExistingDaemons",
		"Restore-Daemons",
		"Find-NpmCommand",
		"npm uninstall -g @yoooclaw/cli",
		"KeepNpm",
		"YOOOCLAW_ACTIVATE_OWNER",
		"__YOOOCLAW_CLI_OSS_BASE_URL__",
		"__YOOOCLAW_CLI_TEMPLATE_RENDERED__",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install.ps1 missing %q", want)
		}
	}
	for _, unwanted := range []string{"npm install -g", "npm i -g", "node.exe"} {
		if strings.Contains(strings.ToLower(script), unwanted) {
			t.Errorf("install.ps1 unexpectedly depends on %q", unwanted)
		}
	}
}

func TestWindowsInstallerMigratesNpmAfterNativeVerification(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(mustAbs(t, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(text)
	verifiedAt := strings.Index(script, `if ($installedVersion -ne $resolvedVersion)`)
	committedAt := strings.Index(script, `$installationCommitted = $true`)
	removeAt := strings.Index(script, `$npmRemoved = Remove-NpmCli $npmCommand`)
	if verifiedAt < 0 || committedAt < 0 || removeAt < 0 {
		t.Fatalf("install.ps1 is missing npm migration ordering markers")
	}
	if !(verifiedAt < committedAt && committedAt < removeAt) {
		t.Fatalf("npm cleanup must happen after native verification and commit: verify=%d commit=%d remove=%d", verifiedAt, committedAt, removeAt)
	}
}

func TestInstallScriptUpdateRestoresRunningCLIOwner(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mockBin := filepath.Join(root, "mock-bin")
	installDir := filepath.Join(root, "install-bin")
	commandLog := filepath.Join(root, "commands.log")
	mustMkdirAll(t, mockBin)
	mustMkdirAll(t, installDir)
	mustMkdirAll(t, filepath.Join(root, ".yoooclaw", "profiles", "default"))

	writeExecutable(t, filepath.Join(mockBin, "uname"), `#!/bin/sh
case "$1" in
  -s) printf 'Darwin\n' ;;
  -m) printf 'arm64\n' ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(installDir, "yoooclaw"), `#!/bin/sh
printf 'old:%s\n' "$*" >> "$COMMAND_LOG"
case "$*" in
  *'daemon status'*) exit 0 ;;
  *'daemon stop'*) exit 0 ;;
esac
printf '0.7.3\n'
`)
	writeExecutable(t, filepath.Join(mockBin, "curl"), `#!/bin/sh
case "$*" in
  *api.github.com*)
    printf '%s\n' '[{"tag_name":"cli-v0.8.1"}]'
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
    {
      printf '%s\n' '#!/bin/sh'
      printf '%s\n' 'printf '\''new:%s\n'\'' "$*" >> "$COMMAND_LOG"'
      printf '%s\n' 'printf '\''0.8.1\n'\'''
    } > "$destination"
    ;;
esac
`)

	cmd := exec.Command("sh", mustAbs(t, "install.sh"), "--dir", installDir, "--force")
	cmd.Env = append(os.Environ(),
		"HOME="+root,
		"SHELL=/bin/zsh",
		"COMMAND_LOG="+commandLog,
		"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh update failed: %v\n%s", err, output)
	}
	logBytes, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	for _, want := range []string{
		"old:--profile default daemon status",
		"old:--profile default daemon stop",
		"new:--profile default daemon autostart enable",
		"new:daemon autostart migrate --format json",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("update did not preserve CLI owner (%q missing):\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "owner activate cli") {
		t.Fatalf("normal update unexpectedly switched owner:\n%s", logText)
	}
}

func TestNpmPackagePreservesOwnerUnlessExplicitlyActivated(t *testing.T) {
	t.Parallel()
	gen, err := os.ReadFile(mustAbs(t, "gen-pkg.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	activation, err := os.ReadFile(mustAbs(t, filepath.Join("..", "npm", "cli", "bin", "activate-owner.js")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gen), `preinstall: "node bin/prepare-owner.js"`) {
		t.Fatal("npm package has no daemon drain preinstall")
	}
	if !strings.Contains(string(gen), `postinstall: "node bin/activate-owner.js"`) {
		t.Fatal("npm package has no owner activation postinstall")
	}
	if !strings.Contains(string(activation), `"owner", "activate", "cli"`) {
		t.Fatal("npm postinstall does not activate CLI owner")
	}
	for _, want := range []string{"YOOOCLAW_ACTIVATE_OWNER", "runningProfiles", "daemon\", \"autostart\", \"migrate", "current Relay owner preserved"} {
		if !strings.Contains(string(activation), want) {
			t.Fatalf("npm postinstall missing owner-preservation marker %q", want)
		}
	}
}

func TestNpmLifecycleRestoresRunningCLIOwnerWithoutSwitching(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is covered by the compiled Windows owner-lock tests")
	}

	root := t.TempDir()
	profileDir := filepath.Join(root, "profiles", "default")
	nodeModules := filepath.Join(root, "node_modules")
	commandLog := filepath.Join(root, "commands.log")
	mustMkdirAll(t, profileDir)

	oldCLI := filepath.Join(root, "old-yoooclaw")
	writeExecutable(t, oldCLI, `#!/bin/sh
printf 'old:%s\n' "$*" >> "$COMMAND_LOG"
case "$*" in
  *'daemon status'*) exit 0 ;;
  *'daemon stop'*) exit 0 ;;
esac
exit 1
`)
	if err := os.WriteFile(
		filepath.Join(profileDir, "daemon.lock"),
		[]byte(`{"pid":123,"executable":"`+oldCLI+`"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	npmOS := runtime.GOOS
	npmCPU := runtime.GOARCH
	if npmCPU == "amd64" {
		npmCPU = "x64"
	}
	nativeCLI := filepath.Join(nodeModules, "@yoooclaw", "cli-"+npmOS+"-"+npmCPU, "bin", "yc")
	mustMkdirAll(t, filepath.Dir(nativeCLI))
	writeExecutable(t, nativeCLI, `#!/bin/sh
printf 'new:%s\n' "$*" >> "$COMMAND_LOG"
exit 0
`)

	env := append(os.Environ(),
		"YOOOCLAW_HOME="+root,
		"NODE_PATH="+nodeModules,
		"COMMAND_LOG="+commandLog,
	)
	for _, script := range []string{
		filepath.Join("..", "npm", "cli", "bin", "prepare-owner.js"),
		filepath.Join("..", "npm", "cli", "bin", "activate-owner.js"),
	} {
		cmd := exec.Command("node", mustAbs(t, script))
		cmd.Env = env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", script, err, output)
		}
	}

	logBytes, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	for _, want := range []string{
		"old:--profile default daemon status",
		"old:--profile default daemon stop",
		"new:--profile default daemon autostart enable",
		"new:daemon autostart migrate --format json",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("npm lifecycle did not restore CLI owner (%q missing):\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "owner activate cli") {
		t.Fatalf("npm update unexpectedly switched owner:\n%s", logText)
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

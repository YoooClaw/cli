package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Execute the actual installer fragment under strict PowerShell, including
// responses that omit optional fields. No installation or task changes.
func TestWindowsMigrationOptionalFields(t *testing.T) {
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		shell, err = exec.LookPath("pwsh")
	}
	if err != nil {
		t.Skip("requires PowerShell")
	}
	raw, err := os.ReadFile(mustAbs(t, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	start := strings.Index(script, "    $taskMigrationConfirmed = $false")
	end := strings.Index(script, `    Write-Info "Daemon login-autostart state checked."`)
	if start < 0 || end < start {
		t.Fatal("migration confirmation fragment not found")
	}
	for _, tc := range []struct {
		json string
		want bool
	}{
		{`{"ok":true,"migrated":false,"reason":"uninitialized"}`, false},
		{`{"ok":true,"migrated":false,"desired":"disabled"}`, false},
		{`{"ok":true}`, false},
		{`{"ok":true,"migrated":true}`, true},
		{`{"ok":true,"repaired":true}`, true},
		{`{"migrated":false,"repaired":true}`, true},
		{`{"migrated":false,"repaired":false}`, false},
		{`{"migrated":"true","repaired":null}`, false},
	} {
		want := "$false"
		if tc.want {
			want = "$true"
		}
		command := "Set-StrictMode -Version 2.0; $ErrorActionPreference='Stop'; $migrationResult = '" + tc.json + "' | ConvertFrom-Json;\n" + script[start:end] + fmt.Sprintf("\nif ($taskMigrationConfirmed -ne %s) { throw 'unexpected confirmation' }", want)
		if out, err := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", tc.json, err, out)
		}
	}
}

func TestInstallScriptResolvesGitHubVersionPortably(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		args             []string
		wantVersion      string
		wantPathModified bool
		wantActivated    bool
	}{
		// The mocked GitHub API lists the prerelease first: resolving without
		// --version must still skip it and land on the newest stable tag.
		{name: "stable", wantVersion: "0.7.2"},
		{name: "stable with legacy opt-out", args: []string{"--no-modify-path"}, wantVersion: "0.7.2"},
		{name: "explicit prerelease", args: []string{"--version", "0.8.0-beta.1"}, wantVersion: "0.8.0-beta.1"},
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

func TestInstallersRejectBetaChannelFlag(t *testing.T) {
	t.Parallel()

	// Prereleases are reachable only through an explicit version: no channel
	// flag may resolve one, so the default install path stays on stable.
	for _, script := range []string{"install.sh", "install-wuying.sh"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command("sh", mustAbs(t, script), "--beta")
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s unexpectedly accepted --beta:\n%s", script, output)
			}
			if !strings.Contains(string(output), "beta 版必须显式指定版本号") {
				t.Fatalf("%s did not explain how to install a prerelease:\n%s", script, output)
			}
		})
	}
}

func TestInstallScriptReportsInheritedPathForAgents(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "present", "shadowed", "modify-path", "relative-dir"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mockBin := filepath.Join(root, "mock-bin")
			// Exercise quoting as well as absolute symlinks for --dir.
			installDir := filepath.Join(root, "install bin $literal")
			mustMkdirAll(t, mockBin)
			writeExecutable(t, filepath.Join(mockBin, "uname"), `#!/bin/sh
case "$1" in
  -s) printf 'Linux\n' ;;
  -m) printf 'x86_64\n' ;;
esac
`)
			writeExecutable(t, filepath.Join(mockBin, "curl"), `#!/bin/sh
case "$*" in *checksums.txt*) exit 22 ;; esac
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then
    printf '%s\n' '#!/bin/sh' 'printf "0.7.2\n"' > "$2"
    exit 0
  fi
  shift
done
exit 2
`)
			inheritedPath := mockBin + ":/usr/bin:/bin"
			if mode == "present" {
				inheritedPath = installDir + ":" + inheritedPath
			}
			if mode == "shadowed" {
				writeExecutable(t, filepath.Join(mockBin, "yoooclaw"), "#!/bin/sh\nexit 1\n")
			}
			dirArg := installDir
			if mode == "relative-dir" {
				dirArg = filepath.Base(installDir)
			}
			args := []string{mustAbs(t, "install.sh"), "--version", "0.7.2", "--dir", dirArg}
			if mode == "modify-path" {
				args = append(args, "--modify-path")
			}
			env := append(os.Environ(), "HOME="+root, "SHELL=/bin/bash", "PATH="+inheritedPath, "BASH_ENV=", "YOOOCLAW_ACTIVATE_OWNER=")
			cmd := exec.Command("sh", args...)
			cmd.Dir = root
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			want := "安装时继承的 PATH 找不到 yoooclaw"
			if mode == "present" {
				want = "安装时继承的 PATH 可找到本次安装"
			} else if mode == "shadowed" {
				want = "安装时继承的 PATH 优先找到其他 yoooclaw"
			}
			for _, message := range []string{want, "二进制验证通过:", "Agent runner/服务启动环境中配置 PATH"} {
				if !strings.Contains(string(output), message) {
					t.Fatalf("missing %q:\n%s", message, output)
				}
			}
			// Reproduce an Agent's fresh bash -c using the unchanged parent PATH.
			if mode == "missing" || mode == "modify-path" {
				probe := exec.Command("bash", "--noprofile", "--norc", "-c", "command -v yoooclaw")
				probe.Env = env
				if out, err := probe.CombinedOutput(); err == nil {
					t.Fatalf("Agent unexpectedly discovered command: %s", out)
				}
			}
			// The printed fallback must work verbatim, including special characters.
			for _, line := range strings.Split(string(output), "\n") {
				if _, command, ok := strings.Cut(line, "完整路径调用（不依赖 PATH）: "); ok {
					probe := exec.Command("sh", "-c", command)
					probe.Env = env
					if out, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "0.7.2" {
						t.Fatalf("fallback failed: %v\n%s", err, out)
					}
				}
			}
			probe := exec.Command(filepath.Join(installDir, "yc"), "--version")
			probe.Dir = "/"
			if out, err := probe.CombinedOutput(); err != nil {
				t.Fatalf("yc symlink failed outside install cwd: %v\n%s", err, out)
			}
		})
	}
}

func TestWindowsInstallerRejectsBetaChannelSwitch(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(mustAbs(t, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(text)
	if !strings.Contains(script, `throw "beta 版必须显式指定版本号`) {
		t.Error("install.ps1 must reject -Beta instead of resolving a prerelease channel")
	}
	if strings.Contains(script, `$channel = "beta"`) {
		t.Error("install.ps1 must not resolve a beta channel marker")
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
		"$response.Content -is [byte[]]",
		"[Text.Encoding]::UTF8.GetString",
		"YoooClaw\\bin",
		"yoooclaw.exe",
		"yc.exe",
		"SetEnvironmentVariable(\"Path\"",
		"Stop-ExistingDaemons",
		"Restore-Daemons",
		"Remove-StaleYoooClawUninstallFiles -ResolvedInstallDir $InstallDir",
		"yoooclaw-*.exe.pending",
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

func TestWindowsInstallerCanRunThroughPowerShell51InvokeExpression(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(mustAbs(t, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(text)
	childBlockAt := strings.Index(script, "& {")
	cmdletBindingAt := strings.Index(script, "[CmdletBinding()]")
	if childBlockAt < 0 || cmdletBindingAt < 0 || cmdletBindingAt < childBlockAt {
		t.Fatal("install.ps1 must keep CmdletBinding inside a child script block for Windows PowerShell 5.1 Invoke-Expression")
	}
	if !strings.Contains(script, "} @yoooclawInstallerArguments") {
		t.Fatal("install.ps1 must forward script-block arguments to the child installer")
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
	migrateAt := strings.Index(script, `& $target daemon autostart migrate --repair-permissions --format json`)
	restoreAt := strings.Index(script, `Restore-Daemons $target $stoppedProfiles`)
	if migrateAt < committedAt || restoreAt < migrateAt || removeAt < restoreAt {
		t.Fatal("migration must precede daemon restore and npm cleanup")
	}
	if !strings.Contains(script, `throw "Native CLI installed, but task migration failed.`) {
		t.Fatal("migration failure must not report installation success")
	}
	if verifiedAt < 0 || committedAt < 0 || removeAt < 0 {
		t.Fatalf("install.ps1 is missing npm migration ordering markers")
	}
	if !(verifiedAt < committedAt && committedAt < removeAt) {
		t.Fatalf("npm cleanup must happen after native verification and commit: verify=%d commit=%d remove=%d", verifiedAt, committedAt, removeAt)
	}
}

func TestInstallScriptUpdateRestoresRunningCLIOwner(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"Darwin", "Linux", "Linux-no-bus"} {
		t.Run(platform, func(t *testing.T) {
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
  -s) printf '%s\n' "$MOCK_OS" ;;
  -m) printf 'arm64\n' ;;
  *) exit 2 ;;
esac
`)
			writeExecutable(t, filepath.Join(mockBin, "systemctl"), `#!/bin/sh
printf 'systemctl:%s\n' "$*" >> "$COMMAND_LOG"
[ "$MOCK_BUS" = available ] && exit 0
printf 'Failed to connect to bus: No medium found\n' >&2
exit 1
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
			mockOS, mockBus := platform, "available"
			if platform == "Linux-no-bus" {
				mockOS, mockBus = "Linux", "unavailable"
			}
			cmd.Env = append(os.Environ(),
				"MOCK_OS="+mockOS, "MOCK_BUS="+mockBus,
				"HOME="+root,
				"SHELL=/bin/zsh",
				"COMMAND_LOG="+commandLog,
				"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			output, err := cmd.CombinedOutput()
			if platform == "Linux-no-bus" {
				if err == nil || !strings.Contains(string(output), "保留旧 daemon 和二进制") {
					t.Fatalf("expected safe abort: %v\n%s", err, output)
				}
				logBytes, readErr := os.ReadFile(commandLog)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if strings.Contains(string(logBytes), "daemon stop") {
					t.Fatalf("stopped healthy daemon: %s", logBytes)
				}
				oldBytes, readErr := os.ReadFile(filepath.Join(installDir, "yoooclaw"))
				if readErr != nil || !strings.Contains(string(oldBytes), "0.7.3") {
					t.Fatal("old binary was replaced")
				}
				return
			}
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
			if platform == "Linux" && strings.Index(logText, "systemctl:--user show-environment") > strings.Index(logText, "daemon stop") {
				t.Fatal("preflight ran after stop")
			}
		})
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

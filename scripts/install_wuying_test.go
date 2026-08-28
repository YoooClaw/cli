package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWuyingInstallerConfiguresCredentialSkillAndDaemon(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mockBin := filepath.Join(root, "mock-bin")
	installDir := filepath.Join(root, "install-bin")
	commandLog := filepath.Join(root, "commands.log")
	keyLog := filepath.Join(root, "key.log")
	baseInstaller := filepath.Join(root, "base-install.sh")
	fakeCLI := filepath.Join(root, "fake-yoooclaw")
	mustMkdirAll(t, mockBin)

	writeExecutable(t, filepath.Join(mockBin, "curl"), `#!/bin/sh
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
cp "$BASE_INSTALLER_FIXTURE" "$destination"
`)
	writeExecutable(t, baseInstaller, `#!/bin/sh
printf '%s\n' "$*" > "$BASE_ARGS_LOG"
install_dir=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dir) install_dir=$2; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$install_dir" ] || exit 3
mkdir -p "$install_dir"
cp "$FAKE_CLI_FIXTURE" "$install_dir/yoooclaw"
chmod +x "$install_dir/yoooclaw"
`)
	writeExecutable(t, fakeCLI, `#!/bin/sh
printf '%s\n' "$*" >> "$COMMAND_LOG"
case "$*" in
  *'auth set-api-key -'*)
    IFS= read -r api_key
    printf '%s' "$api_key" > "$KEY_LOG"
    ;;
  *'config show'*) exit 1 ;;
  *'daemon status'*) exit 0 ;;
esac
exit 0
`)

	secret := "ock-secret-for-wuying"
	baseArgsLog := filepath.Join(root, "base-args.log")
	cmd := exec.Command(
		"sh", mustAbs(t, "install-wuying.sh"),
		"--api-key", secret,
		"--skill", "claude",
		"--profile", "cloud",
		"--version", "1.2.3",
		"--dir", installDir,
		"--force",
		"--modify-path",
	)
	cmd.Env = append(os.Environ(),
		"HOME="+root,
		"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BASE_INSTALLER_FIXTURE="+baseInstaller,
		"FAKE_CLI_FIXTURE="+fakeCLI,
		"BASE_ARGS_LOG="+baseArgsLog,
		"COMMAND_LOG="+commandLog,
		"KEY_LOG="+keyLog,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-wuying.sh failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), secret) {
		t.Fatalf("installer leaked API key to output:\n%s", output)
	}

	storedKey, err := os.ReadFile(keyLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(storedKey) != secret {
		t.Fatalf("stored key = %q, want supplied key", storedKey)
	}

	baseArgs, err := os.ReadFile(baseArgsLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--version 1.2.3", "--dir " + installDir, "--force", "--modify-path"} {
		if !strings.Contains(string(baseArgs), want) {
			t.Fatalf("base installer did not receive %q:\n%s", want, baseArgs)
		}
	}

	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(commands)
	for _, want := range []string{
		"--profile cloud auth set-api-key -",
		"--profile cloud config init --non-interactive --from-file",
		"--profile cloud skills install --agent claude --force",
		"--profile cloud owner activate cli --no-start",
		"--profile cloud daemon autostart enable",
		"--profile cloud daemon status",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("provisioning command %q missing:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, secret) {
		t.Fatalf("API key leaked into child command arguments:\n%s", logText)
	}
}

func TestWuyingInstallerFallsBackToDetachedDaemon(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mockBin := filepath.Join(root, "mock-bin")
	installDir := filepath.Join(root, "install-bin")
	commandLog := filepath.Join(root, "commands.log")
	statusMarker := filepath.Join(root, "status.checked")
	baseInstaller := filepath.Join(root, "base-install.sh")
	fakeCLI := filepath.Join(root, "fake-yoooclaw")
	mustMkdirAll(t, mockBin)

	writeExecutable(t, filepath.Join(mockBin, "curl"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then destination=$2; shift 2; else shift; fi
done
cp "$BASE_INSTALLER_FIXTURE" "$destination"
`)
	writeExecutable(t, baseInstaller, `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = '--dir' ]; then install_dir=$2; shift 2; else shift; fi
done
mkdir -p "$install_dir"
cp "$FAKE_CLI_FIXTURE" "$install_dir/yoooclaw"
chmod +x "$install_dir/yoooclaw"
`)
	writeExecutable(t, fakeCLI, `#!/bin/sh
printf '%s\n' "$*" >> "$COMMAND_LOG"
case "$*" in
  *'auth set-api-key -'*) IFS= read -r _ ;;
  *'config show'*) exit 0 ;;
  *'daemon autostart enable'*) exit 1 ;;
  *'daemon status'*)
    if [ ! -e "$STATUS_MARKER" ]; then
      : > "$STATUS_MARKER"
      exit 1
    fi
    ;;
esac
exit 0
`)

	cmd := exec.Command(
		"sh", mustAbs(t, "install-wuying.sh"),
		"--api-key", "ock-fallback",
		"--skill", "codex",
		"--dir", installDir,
	)
	cmd.Env = append(os.Environ(),
		"HOME="+root,
		"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BASE_INSTALLER_FIXTURE="+baseInstaller,
		"FAKE_CLI_FIXTURE="+fakeCLI,
		"COMMAND_LOG="+commandLog,
		"STATUS_MARKER="+statusMarker,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-wuying.sh fallback failed: %v\n%s", err, output)
	}
	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "--profile default daemon start") {
		t.Fatalf("detached fallback did not start daemon:\n%s", commands)
	}
	if !strings.Contains(string(output), "detached") {
		t.Fatalf("fallback was not reported:\n%s", output)
	}
}

func TestWuyingInstallerRequiresAPIKeyAndSkill(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing api key", args: []string{"--skill", "claude"}, want: "--api-key 必填"},
		{name: "missing skill", args: []string{"--api-key", "ock-test"}, want: "--skill 必填"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", append([]string{mustAbs(t, "install-wuying.sh")}, tc.args...)...)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("installer unexpectedly accepted incomplete args:\n%s", output)
			}
			if !strings.Contains(string(output), tc.want) {
				t.Fatalf("error does not contain %q:\n%s", tc.want, output)
			}
		})
	}
}

func TestWuyingInstallerHelpContainsOnlyHeader(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", mustAbs(t, "install-wuying.sh"), "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{"--api-key <key>", "--skill <agent>", "--profile <name>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"set -eu", sentinelBaseURLForScriptTest} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("help leaked script body %q:\n%s", unwanted, text)
		}
	}
}

const sentinelBaseURLForScriptTest = "__YOOOCLAW_CLI_OSS_BASE_URL__"

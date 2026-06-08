package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execCLI 在隔离沙箱里执行一次 CLI（捕获 os.Stdout 与退出码）。
// 因为 run() 直接写 os.Stdout 且 exitCode 是包级变量，这些测试不可并行。
func execCLI(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	exitCode = 0

	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	_ = root.Execute()

	_ = w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	return string(data), exitCode
}

// sandbox 设置隔离的 YOOOCLAW_HOME。
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	t.Setenv("YOOOCLAW_PROFILE", "")
	return home
}

func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("output not JSON object: %q (%v)", out, err)
	}
	return m
}

func TestRootHelpAndVersion(t *testing.T) {
	sandbox(t)
	if out, _ := execCLI(t, "--version"); strings.TrimSpace(out) == "" {
		// --version writes via cobra to SetOut(io.Discard); just ensure no crash.
	}
	// help sweep: 构造命令树 + help 不应崩溃
	if _, code := execCLI(t, "--help"); code != 0 {
		t.Errorf("--help exit code = %d", code)
	}
}

func TestHelpSweepAllCommands(t *testing.T) {
	sandbox(t)
	root := newRootCmd()
	// 遍历一层子命令跑 --help，覆盖各命令构造与 usage。
	for _, cmd := range root.Commands() {
		name := strings.Fields(cmd.Use)[0]
		if _, code := execCLI(t, name, "--help"); code != 0 {
			t.Errorf("%s --help exit code = %d", name, code)
		}
	}
}

func TestConfigInitShowSetUnset(t *testing.T) {
	sandbox(t)
	// 准备 from-file 导入
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "import.json")
	os.WriteFile(cfgFile, []byte(`{"daemon":{"port":20001}}`), 0o600)

	out, code := execCLI(t, "config", "init", "--non-interactive", "--from-file", cfgFile, "--no-start")
	if code != 0 {
		t.Fatalf("config init failed (code %d): %s", code, out)
	}
	m := decode(t, out)
	if m["ok"] != true {
		t.Errorf("init not ok: %+v", m)
	}

	// 再次 init 无 force -> ALREADY_EXISTS
	out, code = execCLI(t, "config", "init", "--non-interactive", "--from-file", cfgFile, "--no-start")
	if code == 0 {
		t.Error("second init without --force should fail")
	}
	if errObj := decode(t, out)["error"].(map[string]any); errObj["code"] != "YOOOCLAW_ALREADY_EXISTS" {
		t.Errorf("expected ALREADY_EXISTS, got %v", errObj["code"])
	}

	// show -> masked config
	out, code = execCLI(t, "config", "show")
	if code != 0 {
		t.Fatalf("config show failed: %s", out)
	}
	shown := decode(t, out)
	if _, ok := shown["daemon"]; !ok {
		t.Errorf("config show missing daemon: %+v", shown)
	}

	// set
	out, code = execCLI(t, "config", "set", "daemon.port", "20002")
	if code != 0 {
		t.Fatalf("config set failed: %s", out)
	}
	if decode(t, out)["value"] != float64(20002) {
		t.Errorf("set value wrong: %s", out)
	}

	// set version -> rejected
	if _, code := execCLI(t, "config", "set", "version", "9"); code == 0 {
		t.Error("setting version should fail")
	}

	// unset
	out, code = execCLI(t, "config", "unset", "daemon.port")
	if code != 0 || decode(t, out)["removed"] != true {
		t.Errorf("unset failed: %s", out)
	}
}

func TestConfigShowBeforeInit(t *testing.T) {
	sandbox(t)
	out, code := execCLI(t, "config", "show")
	if code == 0 {
		t.Error("config show before init should fail")
	}
	if decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_CONFIG_INVALID" {
		t.Errorf("expected CONFIG_INVALID: %s", out)
	}
}

func TestInvalidFormatFlag(t *testing.T) {
	sandbox(t)
	out, code := execCLI(t, "config", "show", "--format", "yaml")
	if code == 0 {
		t.Error("invalid --format should fail")
	}
	if !strings.Contains(out, "YOOOCLAW_INVALID_ARGUMENT") {
		t.Errorf("expected INVALID_ARGUMENT: %s", out)
	}
}

func TestLightSendValidation(t *testing.T) {
	sandbox(t)
	// 无 --segments/--preset/--rule -> INVALID_ARGUMENT（不触达 daemon）
	out, code := execCLI(t, "light", "send")
	if code == 0 {
		t.Error("light send without source should fail")
	}
	if decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
		t.Errorf("expected INVALID_ARGUMENT: %s", out)
	}
}

func TestDaemonDependentCommandsWithoutDaemon(t *testing.T) {
	sandbox(t)
	// 这些 🟡 命令在 daemon 未运行时应统一报 DAEMON_NOT_RUNNING
	cases := [][]string{
		{"light", "send", "--preset", "red-steady"},
		{"lightrule", "list"},
		{"tunnel", "status"},
	}
	for _, args := range cases {
		out, code := execCLI(t, args...)
		if code == 0 {
			t.Errorf("%v should fail without daemon", args)
			continue
		}
		errObj := decode(t, out)["error"].(map[string]any)
		if errObj["code"] != "YOOOCLAW_DAEMON_NOT_RUNNING" {
			t.Errorf("%v expected DAEMON_NOT_RUNNING, got %v", args, errObj["code"])
		}
	}
}

func TestSkillsListCLI(t *testing.T) {
	sandbox(t)
	out, code := execCLI(t, "skills", "list")
	if code != 0 {
		t.Fatalf("skills list failed: %s", out)
	}
	// 输出应是 JSON（数组或对象），至少不为空
	if strings.TrimSpace(out) == "" {
		t.Error("skills list produced no output")
	}
}

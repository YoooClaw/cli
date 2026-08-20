package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestLightSendForwardsTitleAndReason(t *testing.T) {
	sandbox(t)
	var requestBody map[string]any
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode light request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"code":"000000","msg":"成功","data":{"success":true}}`,
			)),
			Request: req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	out, code := execCLI(t,
		"light", "send",
		"--preset", "red-strobe-3",
		"--title", "测试灯效",
		"--reason", "插件一次性亮灯",
	)
	if code != 0 {
		t.Fatalf("light send failed (code %d): %s", code, out)
	}
	if got := requestBody["title"]; got != "测试灯效" {
		t.Errorf("title = %v, want 测试灯效", got)
	}
	if got := requestBody["reason"]; got != "插件一次性亮灯" {
		t.Errorf("reason = %v, want 插件一次性亮灯", got)
	}
	if _, ok := requestBody["idempotencyKey"]; ok {
		t.Errorf("light send must not send the undeployed idempotencyKey contract: %+v", requestBody)
	}
}

func TestLightruleCloudValidation(t *testing.T) {
	sandbox(t)
	// 以下用例都在本地参数校验阶段失败，不触达云端。
	cases := []struct {
		args     []string
		wantCode string
	}{
		{[]string{"lightrule", "create"}, "YOOOCLAW_INVALID_ARGUMENT"},                                        // 缺 --intent
		{[]string{"lightrule", "update", "r1"}, "YOOOCLAW_INVALID_ARGUMENT"},                                  // 无更新字段
		{[]string{"lightrule", "update", "r1", "--intent", "改绿灯", "--title", "t"}, "YOOOCLAW_INVALID_ARGUMENT"}, // ruleText 与普通字段混用
		{[]string{"lightrule", "update", "r1", "--repeat-times", "abc"}, "YOOOCLAW_INVALID_ARGUMENT"},
		{[]string{"lightrule", "update", "r1", "--segments", `[{"mode":"nope"}]`}, "VALIDATION_FAILED"},
	}
	for _, tc := range cases {
		out, code := execCLI(t, tc.args...)
		if code == 0 {
			t.Errorf("%v should fail", tc.args)
			continue
		}
		if got := decode(t, out)["error"].(map[string]any)["code"]; got != tc.wantCode {
			t.Errorf("%v expected %s, got %v: %s", tc.args, tc.wantCode, got, out)
		}
	}
}

func TestDaemonDependentCommandsWithoutDaemon(t *testing.T) {
	sandbox(t)
	// 这些 🟡 命令在 daemon 未运行时应统一报 DAEMON_NOT_RUNNING。
	// （light/lightrule 自 daemonless 重构起直连本地文件与灯效云，不再在列。）
	cases := [][]string{
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

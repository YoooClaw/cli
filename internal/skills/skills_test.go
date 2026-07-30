package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

func TestParseSkillMeta(t *testing.T) {
	t.Parallel()
	md := "---\nname: my-skill\ndescription: does things\n---\n# body\n"
	name, desc := parseSkillMeta(md)
	if name != "my-skill" || desc != "does things" {
		t.Errorf("parsed (%q,%q)", name, desc)
	}
	// 无 frontmatter
	n2, d2 := parseSkillMeta("# no frontmatter")
	if n2 != "" || d2 != "" {
		t.Errorf("no frontmatter should yield empties, got (%q,%q)", n2, d2)
	}
}

func TestParseSkillMetaBlockScalar(t *testing.T) {
	t.Parallel()
	md := "---\nname: my-skill\ndescription: |\n  line one\n  line two\n\n  line three\nother: x\n---\n# body\n"
	name, desc := parseSkillMeta(md)
	if name != "my-skill" {
		t.Errorf("name = %q", name)
	}
	want := "line one\nline two\n\nline three"
	if desc != want {
		t.Errorf("block scalar desc = %q, want %q", desc, want)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected embedded skills")
	}
	names := make([]string, len(got))
	for i, b := range got {
		names[i] = b.Name
		if b.Title == "" {
			t.Errorf("skill %q missing title", b.Name)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("skills should be sorted: %v", names)
	}
	want := []string{
		"yoooclaw-context-query",
		"yoooclaw-lightrule-create",
		"yoooclaw-recordings-process",
		"yoooclaw-tunnel-debug",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("bundled skills = %v, want %v", names, want)
	}
}

// isolateHome 把 HOME / CODEX_HOME 指向临时目录，隔离 agent 探测。
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	return home
}

func TestTargetsNoneDetected(t *testing.T) {
	isolateHome(t)
	for _, c := range Targets() {
		if c.Detected {
			t.Errorf("agent %q should not be detected in clean home", c.Agent)
		}
	}
}

func TestTargetsClaudeDetected(t *testing.T) {
	home := isolateHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	var claudeDetected bool
	for _, c := range Targets() {
		if c.Agent == "claude" {
			claudeDetected = c.Detected
		}
	}
	if !claudeDetected {
		t.Error("claude should be detected when ~/.claude exists")
	}
}

func TestResolveTargetInvalidAgent(t *testing.T) {
	t.Parallel()
	_, err := ResolveTarget("bogus", "")
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeInvalidArgument {
		t.Errorf("want INVALID_ARGUMENT, got %v", err)
	}
}

func TestResolveTargetExplicit(t *testing.T) {
	t.Parallel()
	sel, err := ResolveTarget("auto", "/custom/dir")
	if err != nil || sel.Agent != "custom" || sel.Target != "/custom/dir" {
		t.Errorf("explicit auto->custom: %+v err=%v", sel, err)
	}
	sel, err = ResolveTarget("claude", "/x")
	if err != nil || sel.Agent != "claude" || sel.Target != "/x" {
		t.Errorf("explicit claude: %+v err=%v", sel, err)
	}
}

func TestResolveTargetCustomWithoutTarget(t *testing.T) {
	t.Parallel()
	_, err := ResolveTarget("custom", "")
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeInvalidArgument {
		t.Errorf("custom without target should error: %v", err)
	}
}

func TestResolveTargetAgentDefault(t *testing.T) {
	home := isolateHome(t)
	sel, err := ResolveTarget("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Source != "agent-default" || sel.Target != filepath.Join(home, ".codex", "skills") {
		t.Errorf("agent-default mismatch: %+v", sel)
	}
}

func TestResolveTargetAutoNoneDetected(t *testing.T) {
	isolateHome(t)
	_, err := ResolveTarget("auto", "")
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeNotFound {
		t.Errorf("auto none -> NOT_FOUND, got %v", err)
	}
}

func TestResolveTargetAutoSingle(t *testing.T) {
	home := isolateHome(t)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	sel, err := ResolveTarget("auto", "")
	if err != nil || sel.Agent != "claude" || sel.Source != "auto-detected" {
		t.Errorf("auto single: %+v err=%v", sel, err)
	}
}

func TestResolveTargetAutoMultiple(t *testing.T) {
	home := isolateHome(t)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	_, err := ResolveTarget("auto", "")
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.CodeConfirmationRequired {
		t.Errorf("auto multiple -> CONFIRMATION_REQUIRED, got %v", err)
	}
}

func TestInstall(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "skills")
	results, installed, err := Install(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) == 0 || len(results) == 0 {
		t.Fatal("expected installs")
	}
	// 验证至少一个 SKILL.md 真被解包
	first := results[0]
	if first.Status != "installed" {
		t.Errorf("first should be installed: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(first.Dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not extracted: %v", err)
	}

	// 再次安装：全部 skipped
	results2, _, err := Install(target, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results2 {
		if r.Status != "skipped" {
			t.Errorf("expected skipped on re-install: %+v", r)
		}
	}

	// force：重新安装
	results3, _, err := Install(target, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results3 {
		if r.Status != "installed" {
			t.Errorf("force should reinstall: %+v", r)
		}
	}
}

func TestInstallRemovesMergedLegacySkills(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range deprecatedBundledSkills {
		dir := filepath.Join(target, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(target, "my-personal-skill")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}

	results, _, err := Install(target, false)
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, result := range results {
		if result.Status == "removed" {
			removed[result.Name] = true
		}
	}
	for _, name := range deprecatedBundledSkills {
		if fsutil.Exists(filepath.Join(target, name)) {
			t.Errorf("deprecated skill %q was not removed", name)
		}
		if !removed[name] {
			t.Errorf("deprecated skill %q missing removed result", name)
		}
	}
	if !fsutil.Exists(unrelated) {
		t.Error("unrelated personal skill should remain")
	}
}

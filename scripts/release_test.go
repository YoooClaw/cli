package scripts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type releaseFixture struct {
	t                   *testing.T
	work, remote, ghLog string
	env                 []string
}

func newReleaseFixture(t *testing.T, version string) *releaseFixture {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("requires bash")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("requires node")
	}
	root := t.TempDir()
	f := &releaseFixture{t: t, work: filepath.Join(root, "work"), remote: filepath.Join(root, "remote.git"), ghLog: filepath.Join(root, "gh.log")}
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{f.work, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fake := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GH_TEST_LOG\"\n")
	if err := os.WriteFile(filepath.Join(bin, "gh"), fake, 0700); err != nil {
		t.Fatal(err)
	}
	f.env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GH_TEST_LOG="+f.ghLog, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "no-global"), "GIT_TERMINAL_PROMPT=0")
	f.git("init", "--bare", f.remote)
	f.git("init", "--initial-branch=master")
	f.git("config", "user.email", "release-test@example.invalid")
	f.git("config", "user.name", "Release test")
	raw, err := os.ReadFile(filepath.Join("..", "release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.work, "release.sh"), raw, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.work, "package.json"), []byte("{\n  \"name\": \"@yoooclaw/cli\",\n  \"version\": \""+version+"\"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.git("add", ".")
	f.git("commit", "-m", "initial")
	f.git("remote", "add", "origin", f.remote)
	f.git("push", "-u", "origin", "master")
	f.git("checkout", "-b", "release/0.2.0")
	f.git("push", "-u", "origin", "release/0.2.0")
	hook := `#!/bin/bash
while read -r old new ref; do
 if [[ "$ref" == refs/heads/release/* && "$old" != 0000000000000000000000000000000000000000 ]]; then
  echo "direct release updates rejected" >&2
  exit 1
 fi
done
`
	if err := os.WriteFile(filepath.Join(f.remote, "hooks", "pre-receive"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *releaseFixture) command(name string, args ...string) (string, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = f.work
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
func (f *releaseFixture) git(args ...string) string {
	f.t.Helper()
	out, err := f.command("git", args...)
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}
func TestReleaseVersionBumpUsesPRThenPushesOnlyMergedTag(t *testing.T) {
	f := newReleaseFixture(t, "0.1.0")
	if out, err := f.command("bash", "release.sh", "--push"); err != nil {
		t.Fatalf("release bump: %v\n%s", err, out)
	}
	if got := f.git("branch", "--show-current"); got != "codex/release-0.2.0-version" {
		t.Fatal(got)
	}
	raw, err := os.ReadFile(f.ghLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "pr\ncreate\n--base\nrelease/0.2.0\n--head\ncodex/release-0.2.0-version") {
		t.Fatalf("wrong PR args: %s", raw)
	}
	if refs := f.git("ls-remote", "--tags", "origin"); refs != "" {
		t.Fatalf("tag created before version PR merge: %s", refs)
	}
	if version := f.git("--git-dir", f.remote, "show", "refs/heads/release/0.2.0:package.json"); !strings.Contains(version, "0.1.0") {
		t.Fatalf("release branch changed directly: %s", version)
	}
	// Simulate a server-side PR merge. Direct pushes to release remain rejected by the hook.
	f.git("--git-dir", f.remote, "update-ref", "refs/heads/release/0.2.0", f.git("rev-parse", "HEAD"))
	f.git("checkout", "release/0.2.0")
	f.git("fetch", "origin")
	f.git("merge", "--ff-only", "origin/release/0.2.0")
	f.git("tag", "-a", "unrelated-local-tag", "-m", "must stay local")
	if out, err := f.command("bash", "release.sh", "--push"); err != nil {
		t.Fatalf("release merged version: %v\n%s", err, out)
	}
	refs := f.git("ls-remote", "--tags", "origin")
	if !strings.Contains(refs, "refs/tags/cli-v0.2.0") || strings.Contains(refs, "unrelated-local-tag") {
		t.Fatalf("wrong published tags: %s", refs)
	}
	if got := f.git("rev-parse", "cli-v0.2.0^{}"); got != f.git("rev-parse", "origin/release/0.2.0") {
		t.Fatal("tag does not point to merged release")
	}
}
func TestReleaseRejectsUnmergedLocalCommitBeforeTagging(t *testing.T) {
	f := newReleaseFixture(t, "0.2.0")
	f.git("commit", "--allow-empty", "-m", "not reviewed")
	if out, err := f.command("bash", "release.sh", "--push"); err == nil {
		t.Fatalf("accepted unmerged commit: %s", out)
	}
	if tags := f.git("tag", "--list"); tags != "" {
		t.Fatalf("created tag before rejecting local commit: %s", tags)
	}
}

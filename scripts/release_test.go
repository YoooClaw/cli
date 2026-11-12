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

func (f *releaseFixture) setEnv(key, value string) {
	filtered := make([]string, 0, len(f.env)+1)
	for _, entry := range f.env {
		if !strings.HasPrefix(entry, key+"=") {
			filtered = append(filtered, entry)
		}
	}
	f.env = append(filtered, key+"="+value)
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
	fake := []byte(`#!/bin/sh
printf '%s\n' "$@" >> "$GH_TEST_LOG"
printf '%s\n' '--next-call--' >> "$GH_TEST_LOG"
case "$1 $2" in
 'pr create') printf '%s\n' 'https://example.invalid/pull/1' ;;
 'pr checks')
  case " $* " in
   *' --json '*) printf '2\n' ;;
   *) [ "${GH_TEST_CHECKS_FAIL:-0}" != 1 ] ;;
  esac ;;
 'pr merge')
  [ "${GH_TEST_MERGE_FAIL:-0}" != 1 ] || exit 1
  if [ -n "$(git --git-dir "$GH_TEST_REMOTE" for-each-ref refs/tags/cli-v*)" ]; then
   echo 'tag created before CI and merge' >&2
   exit 1
  fi
  tree=$(git --git-dir "$GH_TEST_REMOTE" rev-parse refs/heads/codex/release-0.2.0-version^{tree})
  parent=$(git --git-dir "$GH_TEST_REMOTE" rev-parse refs/heads/release/0.2.0)
  merged=$(printf 'cli-0.2.0\n\nfixture squash commit\n' | git --git-dir "$GH_TEST_REMOTE" commit-tree "$tree" -p "$parent") || exit 1
  git --git-dir "$GH_TEST_REMOTE" update-ref refs/heads/release/0.2.0 "$merged" || exit 1
  printf '%s\n' "$merged" > "$GH_TEST_MERGED_HEAD"
  if [ "${GH_TEST_BASE_ADVANCES:-0}" = 1 ]; then
   advanced=$(printf 'another approved change\n' | git --git-dir "$GH_TEST_REMOTE" commit-tree "$tree" -p "$merged") || exit 1
   git --git-dir "$GH_TEST_REMOTE" update-ref refs/heads/release/0.2.0 "$advanced" || exit 1
  fi ;;
 'pr view') cat "$GH_TEST_MERGED_HEAD" ;;
 *) echo 'unexpected gh invocation' >&2; exit 1 ;;
esac
`)

	if err := os.WriteFile(filepath.Join(bin, "gh"), fake, 0700); err != nil {
		t.Fatal(err)
	}
	f.env = os.Environ()
	f.setEnv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.setEnv("GH_TEST_LOG", f.ghLog)
	f.setEnv("GH_TEST_REMOTE", f.remote)
	f.setEnv("GH_TEST_MERGED_HEAD", filepath.Join(root, "merged-head"))
	f.setEnv("GIT_CONFIG_NOSYSTEM", "1")
	f.setEnv("GIT_CONFIG_GLOBAL", filepath.Join(root, "no-global"))
	f.setEnv("GIT_TERMINAL_PROMPT", "0")
	f.git("init", "--bare", f.remote)
	f.git("--git-dir", f.remote, "config", "user.email", "release-test@example.invalid")
	f.git("--git-dir", f.remote, "config", "user.name", "Release test")
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
func TestReleaseVersionBumpAutomaticallyMergesAndPushesOnlyMergedTag(t *testing.T) {
	f := newReleaseFixture(t, "0.1.0")
	f.git("tag", "-a", "unrelated-local-tag", "-m", "must stay local")
	if out, err := f.command("bash", "release.sh", "--push"); err != nil {
		t.Fatalf("single-command release: %v\n%s", err, out)
	}
	if got := f.git("branch", "--show-current"); got != "release/0.2.0" {
		t.Fatal(got)
	}
	raw, err := os.ReadFile(f.ghLog)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	if !strings.Contains(calls, "pr\ncreate\n--base\nrelease/0.2.0\n--head\ncodex/release-0.2.0-version") ||
		!strings.Contains(calls, "--required\n--watch\n--fail-fast") || !strings.Contains(calls, "--squash\n--subject\ncli-0.2.0\n--match-head-commit\n"+f.git("rev-parse", "codex/release-0.2.0-version")) || strings.Contains(calls, "--admin") {
		t.Fatalf("wrong automatic PR flow: %s", raw)
	}
	refs := f.git("ls-remote", "--tags", "origin")
	if !strings.Contains(refs, "refs/tags/cli-v0.2.0") || strings.Contains(refs, "unrelated-local-tag") {
		t.Fatalf("wrong published tags: %s", refs)
	}
	merged := f.git("rev-parse", "origin/release/0.2.0")
	if got := f.git("rev-parse", "cli-v0.2.0^{}"); got != merged {
		t.Fatal("tag does not point to merged release")
	}
	if merged == f.git("rev-parse", "codex/release-0.2.0-version") {
		t.Fatal("fixture must simulate a distinct squash commit")
	}
}

func TestReleaseAutomaticPRFailureDoesNotPublishTag(t *testing.T) {
	for _, setting := range []string{"GH_TEST_CHECKS_FAIL", "GH_TEST_MERGE_FAIL"} {
		t.Run(setting, func(t *testing.T) {
			f := newReleaseFixture(t, "0.1.0")
			original := f.git("rev-parse", "origin/release/0.2.0")
			f.setEnv(setting, "1")
			if out, err := f.command("bash", "release.sh", "--push"); err == nil {
				t.Fatalf("accepted failed PR flow: %s", out)
			}
			if refs := f.git("ls-remote", "--tags", "origin"); refs != "" {
				t.Fatalf("tag published after failed PR flow: %s", refs)
			}
			if f.git("tag", "--list") != "" {
				t.Fatal("local tag created after failed PR flow")
			}
			if f.git("--git-dir", f.remote, "rev-parse", "refs/heads/release/0.2.0") != original {
				t.Fatal("release branch changed after failed PR flow")
			}
			calls, err := os.ReadFile(f.ghLog)
			if err != nil {
				t.Fatal(err)
			}
			if setting == "GH_TEST_CHECKS_FAIL" && strings.Contains(string(calls), "pr\nmerge\n") {
				t.Fatal("merge attempted after failed CI")
			}
		})
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

func (f *releaseFixture) expectRejectedWithoutMutation(args []string, want string) {
	f.t.Helper()
	head := f.git("rev-parse", "HEAD")
	branch := f.git("branch", "--show-current")
	refs := f.git("ls-remote", "origin")
	localRefs := f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags")
	packageBefore, err := os.ReadFile(filepath.Join(f.work, "package.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	output, err := f.command("bash", append([]string{"release.sh"}, args...)...)
	if err == nil || !strings.Contains(output, want) {
		f.t.Fatalf("expected rejection containing %q, got err=%v\n%s", want, err, output)
	}
	packageAfter, err := os.ReadFile(filepath.Join(f.work, "package.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	if f.git("rev-parse", "HEAD") != head || f.git("branch", "--show-current") != branch || f.git("ls-remote", "origin") != refs || f.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags") != localRefs || string(packageBefore) != string(packageAfter) {
		f.t.Fatal("rejected release changed commit, branch, remote refs, or package version")
	}
	if _, err := os.Stat(f.ghLog); !os.IsNotExist(err) {
		f.t.Fatalf("PR creation unexpectedly attempted: %v", err)
	}
}

func TestReleaseRejectsMissingGHBeforeCreatingVersionBranch(t *testing.T) {
	f := newReleaseFixture(t, "0.1.0")
	// Use the real prerequisite executables but no gh, independently of the host PATH.
	bin := t.TempDir()
	for _, name := range []string{"git", "node", "dirname", "perl"} {
		source, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(source, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	f.setEnv("PATH", bin)
	f.expectRejectedWithoutMutation([]string{"--push"}, "创建版本 PR 需要 gh")
	if _, err := f.command("git", "show-ref", "--verify", "refs/heads/codex/release-0.2.0-version"); err == nil {
		t.Fatal("created version branch without gh")
	}
}

func TestReleaseRejectsDirtyWorktreeBeforeCreatingVersionBranch(t *testing.T) {
	f := newReleaseFixture(t, "0.1.0")
	path := filepath.Join(f.work, "pending.txt")
	if err := os.WriteFile(path, []byte("preserve pending work"), 0600); err != nil {
		t.Fatal(err)
	}
	f.expectRejectedWithoutMutation([]string{"--push"}, "工作区不干净")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve pending work" {
		t.Fatal("changed pending work")
	}
}

func TestReleaseRejectsExistingVersionBranch(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			f := newReleaseFixture(t, "0.1.0")
			versionBranch := "codex/release-0.2.0-version"
			if remote {
				f.git("--git-dir", f.remote, "update-ref", "refs/heads/"+versionBranch, f.git("rev-parse", "HEAD"))
			} else {
				f.git("branch", versionBranch)
			}
			f.expectRejectedWithoutMutation([]string{"--push"}, "版本分支 "+versionBranch+" 已存在")
		})
	}
}

func TestReleaseRejectsExistingTag(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			f := newReleaseFixture(t, "0.2.0")
			f.git("tag", "-a", "cli-v0.2.0", "-m", "existing release")
			if remote {
				f.git("push", "origin", "refs/tags/cli-v0.2.0")
				f.git("tag", "-d", "cli-v0.2.0")
			}
			f.expectRejectedWithoutMutation([]string{"--push"}, "tag cli-v0.2.0 已存在")
		})
	}
}

func TestReleaseRejectsExistingReleaseBranch(t *testing.T) {
	f := newReleaseFixture(t, "0.1.0")
	f.git("checkout", "master")
	f.expectRejectedWithoutMutation([]string{"0.2.0", "--push"}, "分支 release/0.2.0 已存在")
}

func TestReleaseTagPushFailurePreservesTagAndExplainsRetry(t *testing.T) {
	f := newReleaseFixture(t, "0.2.0")
	hook := `#!/bin/bash
while read -r old new ref; do
 if [[ "$ref" == refs/tags/* ]]; then
  echo "tag creation requires administrator" >&2
  exit 1
 fi
done
`
	if err := os.WriteFile(filepath.Join(f.remote, "hooks", "pre-receive"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := f.command("bash", "release.sh", "--push")
	if err == nil || !strings.Contains(output, "本地 tag cli-v0.2.0 已保留") || !strings.Contains(output, "可直接重试 git push origin refs/tags/cli-v0.2.0") {
		t.Fatalf("missing retry guidance: error=%v\n%s", err, output)
	}
	if f.git("tag", "--list") != "cli-v0.2.0" {
		t.Fatal("local tag not preserved")
	}
	if refs := f.git("ls-remote", "--tags", "origin"); refs != "" {
		t.Fatalf("rejected tag was published: %s", refs)
	}
	output, err = f.command("bash", "release.sh", "--push")
	if err == nil || !strings.Contains(output, "确认该 tag 仅在本地且未发布后，可 git tag -d cli-v0.2.0 再重跑") {
		t.Fatalf("missing existing-tag guidance: error=%v\n%s", err, output)
	}
	if f.git("tag", "--list") != "cli-v0.2.0" {
		t.Fatal("retry removed local tag")
	}
}

func TestReleaseStopsIfBaseAdvancesAfterAutomaticMerge(t *testing.T) {
	f := newReleaseFixture(t, "0.1.0")
	f.setEnv("GH_TEST_BASE_ADVANCES", "1")
	output, err := f.command("bash", "release.sh", "--push")
	if err == nil || !strings.Contains(output, "版本 PR 合入后已有其他更新") {
		t.Fatalf("accepted unexpected release head: %v\n%s", err, output)
	}
	if refs := f.git("ls-remote", "--tags", "origin"); refs != "" {
		t.Fatalf("tag published on unexpected head: %s", refs)
	}
	if f.git("tag", "--list") != "" {
		t.Fatal("created local tag on unexpected head")
	}
}

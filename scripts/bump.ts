/**
 * @yoooclaw/cli 版本号自增脚本。
 *
 * 与 phone-notifications/scripts/bump.ts 形为一致，区别仅在 tag 前缀（`cli-v`）
 * 与触发的 workflow（release-cli.yml）。插件那条 release.yml 用 `v*` 通配，
 * 与 `cli-v*` 互斥，不会被这里的 tag 牵连。
 *
 * 用法：bun scripts/bump.ts <patch|minor|major|beta>
 *   patch  预发布转正(0.0.6-beta.2 -> 0.0.6) 或 稳定版 patch+1
 *   minor  次版本 +1
 *   major  主版本 +1
 *   beta   预发布递增(beta.2 -> beta.3) 或 稳定版进入下一个 patch 的 beta.0
 */
import pkg from "../package.json" with { type: "json" };

const kind = process.argv[2];
const allowed = new Set(["patch", "minor", "major", "beta"]);
if (!kind || !allowed.has(kind)) {
  console.error("用法: bun scripts/bump.ts <patch|minor|major|beta>");
  process.exit(1);
}

const current = (pkg as { version: string }).version;
const m = current.match(/^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/);
if (!m) {
  console.error(`无法解析版本号: ${current}`);
  process.exit(1);
}
const major = Number(m[1]);
const minor = Number(m[2]);
const patch = Number(m[3]);
const pre = m[4];
const isPre = pre !== undefined;

function computeTarget(): string {
  switch (kind) {
    case "major":
      return `${major + 1}.0.0`;
    case "minor":
      return `${major}.${minor + 1}.0`;
    case "patch":
      return isPre ? `${major}.${minor}.${patch}` : `${major}.${minor}.${patch + 1}`;
    case "beta": {
      const bm = pre?.match(/^beta\.(\d+)$/);
      if (bm) return `${major}.${minor}.${patch}-beta.${Number(bm[1]) + 1}`;
      if (isPre) return `${major}.${minor}.${patch}-beta.0`;
      return `${major}.${minor}.${patch + 1}-beta.0`;
    }
    default:
      throw new Error("unreachable");
  }
}

const target = computeTarget();
const tag = `cli-v${target}`;

function run(cmd: string[]): void {
  const p = Bun.spawnSync(cmd, { stdio: ["inherit", "inherit", "inherit"] });
  if (p.exitCode !== 0) {
    console.error(`命令失败: ${cmd.join(" ")}`);
    process.exit(p.exitCode ?? 1);
  }
}

function capture(cmd: string[]): string {
  const p = Bun.spawnSync(cmd);
  if (p.exitCode !== 0) {
    console.error(`命令失败: ${cmd.join(" ")}`);
    process.exit(p.exitCode ?? 1);
  }
  return new TextDecoder().decode(p.stdout).trim();
}

function assertReleasePreconditions(): void {
  const branch = capture(["git", "branch", "--show-current"]);
  if (!branch.startsWith("release/")) {
    console.error(`只能在 release/** 分支执行发布，当前分支: ${branch || "(detached)"}`);
    process.exit(1);
  }

  const status = capture(["git", "status", "--porcelain"]);
  if (status) {
    console.error("工作区不干净，请先提交或暂存当前改动后再发布");
    process.exit(1);
  }

  run(["git", "fetch", "origin", `+refs/heads/${branch}:refs/remotes/origin/${branch}`]);
  const localHead = capture(["git", "rev-parse", "HEAD"]);
  const remoteHead = capture(["git", "rev-parse", `origin/${branch}`]);
  if (localHead !== remoteHead) {
    console.error(`本地 ${branch} 必须与 origin/${branch} 完全一致后才能发布`);
    process.exit(1);
  }
}

assertReleasePreconditions();

console.log(`[bump] @yoooclaw/cli ${current} --[${kind}]--> ${target}`);

run(["bun", "pm", "pkg", "set", `version=${target}`]);
run(["git", "commit", "-m", `cli-${target}`, "--", "package.json"]);
run(["git", "tag", "-a", tag, "-m", target]);
run(["git", "push", "--follow-tags", "origin", "HEAD"]);

console.log(`[bump] 已提交并推送 ${tag}，release-cli.yml 将自动触发`);

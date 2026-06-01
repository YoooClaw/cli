/**
 * skills list / install —— 把随包发布的 SKILL.md 安装到 agent 可发现目录。
 *
 * 背景：这些 Skill 在 openclaw 插件里由 `openclaw.plugin.json` 的 `skills` 字段自动注册；
 * 独立 CLI 形态下它们只随 npm 包躺在 `<pkg>/skills/`，没有宿主帮忙装载。本命令负责把它们
 * 软链（默认）或复制到 agent 的 skills 目录，让 agent 能发现并触发。
 */
import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  symlinkSync,
} from "node:fs";
import { fileURLToPath } from "node:url";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import type { CliContext } from "../context.js";
import { ErrorCode, YoooclawError } from "../errors.js";

const AGENT_IDS = ["claude", "codex"] as const;
const INSTALL_AGENT_IDS = ["auto", "custom", ...AGENT_IDS] as const;

type BuiltInAgentId = (typeof AGENT_IDS)[number];
type InstallAgentId = (typeof INSTALL_AGENT_IDS)[number];

interface AgentAdapter {
  id: BuiltInAgentId;
  label: string;
  homeDir(): string;
  skillsDir(): string;
  envHint?: string;
}

export interface SkillTargetCandidate {
  agent: BuiltInAgentId;
  label: string;
  homeDir: string;
  target: string;
  detected: boolean;
  reason: string;
  installCommand: string;
}

interface TargetSelection {
  agent: BuiltInAgentId | "custom";
  agentLabel: string;
  target: string;
  source: "explicit-target" | "agent-default" | "auto-detected";
  detectedTargets?: SkillTargetCandidate[];
}

function trimEnv(name: string): string | undefined {
  const value = process.env[name]?.trim();
  return value ? value : undefined;
}

function claudeHomeDir(): string {
  return join(homedir(), ".claude");
}

function codexHomeDir(): string {
  return trimEnv("CODEX_HOME") ?? join(homedir(), ".codex");
}

const AGENT_ADAPTERS: AgentAdapter[] = [
  {
    id: "claude",
    label: "Claude Code",
    homeDir: claudeHomeDir,
    skillsDir: () => join(claudeHomeDir(), "skills"),
  },
  {
    id: "codex",
    label: "Codex",
    homeDir: codexHomeDir,
    skillsDir: () => join(codexHomeDir(), "skills"),
    envHint: "CODEX_HOME",
  },
];

function getAgentAdapter(id: BuiltInAgentId): AgentAdapter {
  const adapter = AGENT_ADAPTERS.find((a) => a.id === id);
  if (!adapter) {
    throw new YoooclawError(
      ErrorCode.INVALID_ARGUMENT,
      `不支持的 agent：${id}`,
      { hint: `可选值：${INSTALL_AGENT_IDS.join(" | ")}` },
    );
  }
  return adapter;
}

function normalizeAgent(raw: unknown): InstallAgentId {
  const agent = typeof raw === "string" && raw.trim() ? raw.trim() : "auto";
  if ((INSTALL_AGENT_IDS as readonly string[]).includes(agent)) {
    return agent as InstallAgentId;
  }
  throw new YoooclawError(
    ErrorCode.INVALID_ARGUMENT,
    `不支持的 agent：${agent}`,
    { hint: `可选值：${INSTALL_AGENT_IDS.join(" | ")}` },
  );
}

function hasPath(path: string): boolean {
  return existsSync(path);
}

export function listSkillTargetCandidates(): SkillTargetCandidate[] {
  return AGENT_ADAPTERS.map((adapter) => {
    const homeDir = adapter.homeDir();
    const target = adapter.skillsDir();
    const hasHome = hasPath(homeDir);
    const hasTarget = hasPath(target);
    const envValue = adapter.envHint ? trimEnv(adapter.envHint) : undefined;
    return {
      agent: adapter.id,
      label: adapter.label,
      homeDir,
      target,
      detected: hasHome || hasTarget || Boolean(envValue),
      reason: hasTarget
        ? "skills 目录已存在"
        : hasHome
          ? "agent 配置目录已存在"
          : envValue && adapter.envHint
            ? `${adapter.envHint} 已设置`
            : adapter.envHint
            ? `未发现配置目录；可设置 ${adapter.envHint} 或显式传 --agent ${adapter.id}`
            : `未发现配置目录；可显式传 --agent ${adapter.id}`,
      installCommand: `yoooclaw skills install --agent ${adapter.id}`,
    };
  });
}

function resolveTargetSelection(opts: InstallOpts): TargetSelection {
  const explicitTarget = opts.target?.trim();
  const agent = normalizeAgent(opts.agent);

  if (explicitTarget) {
    if (agent === "auto" || agent === "custom") {
      return {
        agent: "custom",
        agentLabel: "Custom",
        target: explicitTarget,
        source: "explicit-target",
      };
    }
    const adapter = getAgentAdapter(agent);
    return {
      agent,
      agentLabel: adapter.label,
      target: explicitTarget,
      source: "explicit-target",
    };
  }

  if (agent === "custom") {
    throw new YoooclawError(
      ErrorCode.INVALID_ARGUMENT,
      "`--agent custom` 需要同时传 `--target <dir>`",
      { hint: "例如：yoooclaw skills install --agent custom --target ~/.config/agent/skills" },
    );
  }

  if (agent !== "auto") {
    const adapter = getAgentAdapter(agent);
    return {
      agent,
      agentLabel: adapter.label,
      target: adapter.skillsDir(),
      source: "agent-default",
    };
  }

  const candidates = listSkillTargetCandidates();
  const detected = candidates.filter((c) => c.detected);
  if (detected.length === 1) {
    const only = detected[0];
    return {
      agent: only.agent,
      agentLabel: only.label,
      target: only.target,
      source: "auto-detected",
      detectedTargets: detected,
    };
  }

  if (detected.length === 0) {
    throw new YoooclawError(
      ErrorCode.NOT_FOUND,
      "未检测到可安装 Skill 的 Agent",
      {
        hint: "请显式指定 `--agent claude` / `--agent codex`，或使用 `--target <dir>` 指定任意 skills 目录",
        targets: candidates,
      },
    );
  }

  throw new YoooclawError(
    ErrorCode.CONFIRMATION_REQUIRED,
    "检测到多个 Agent，无法自动判断 Skill 应安装到哪里",
    {
      hint: "请显式指定 `--agent claude` / `--agent codex`，或使用 `--target <dir>`",
      targets: detected,
    },
  );
}

/**
 * 候选锚点目录，按可靠性排序，用于向上查找随包发布的 `skills/`。
 *
 * 注意：Bun 打包成 cjs 时会把 `__dirname` / `import.meta.url` 替换成**构建期**绝对路径，
 * 在用户机器上无效；因此优先用运行期入口（require.main / argv[1]，node 已对其做 realpath），
 * `import.meta.url` 仅作 dev/test（未打包，源码直跑）的兜底。
 */
function candidateAnchorDirs(): string[] {
  const out: string[] = [];
  // 1) cjs 产物：入口模块真实路径（如 <pkg>/dist/bin.cjs）
  try {
    const mainFile = typeof require !== "undefined" ? require.main?.filename : undefined;
    if (mainFile) out.push(dirname(mainFile));
  } catch {
    // ESM 下无 require
  }
  // 2) 被执行脚本路径（npm bin 软链 → realpath 还原到真实文件）
  try {
    const argv1 = process.argv[1];
    if (argv1) out.push(dirname(realpathSync(argv1)));
  } catch {
    // ignore
  }
  // 3) dev/test：模块自身路径（源码直跑时有效）
  try {
    const url = import.meta.url;
    if (url) out.push(dirname(fileURLToPath(url)));
  } catch {
    // import.meta 不可用
  }
  return out;
}

/** 目录下是否存在「子目录/SKILL.md」结构 —— 据此判定它是 skills 根。 */
function looksLikeSkillsDir(dir: string): boolean {
  if (!existsSync(dir)) return false;
  return readdirSync(dir, { withFileTypes: true }).some(
    (e) => e.isDirectory() && existsSync(join(dir, e.name, "SKILL.md")),
  );
}

/**
 * 定位随包发布的 skills 目录：从模块所在目录逐级向上找 `skills/`。
 * - 产物：`<pkg>/dist/bin.cjs` → 上一级 `<pkg>/skills`
 * - 源码（dev/test）：`<pkg>/src/commands/skills.ts` → 上溯到 `<pkg>/skills`
 */
export function resolveBundledSkillsDir(): string {
  const checked: string[] = [];
  for (const anchor of candidateAnchorDirs()) {
    let cur = anchor;
    for (let i = 0; i < 6; i += 1) {
      const candidate = join(cur, "skills");
      checked.push(candidate);
      if (looksLikeSkillsDir(candidate)) return candidate;
      const parent = dirname(cur);
      if (parent === cur) break;
      cur = parent;
    }
  }
  throw new YoooclawError(
    "YOOOCLAW_NOT_FOUND",
    "找不到随包发布的 skills 目录",
    {
      hint: "正常安装的 @yoooclaw/cli 应在包根带 skills/；若从源码运行请先 bun run build",
      checkedPaths: checked,
    },
  );
}

export interface BundledSkill {
  name: string;
  dir: string;
  /** frontmatter 的 name 字段（缺省回落目录名） */
  title: string;
  description: string;
}

/** 从 SKILL.md frontmatter 抽取 name / description（容忍多行被截断的情况，只取首行）。 */
function parseSkillMeta(skillMd: string): { name?: string; description?: string } {
  const fm = /^---\r?\n([\s\S]*?)\r?\n---/.exec(skillMd);
  const block = fm ? fm[1] : skillMd;
  const pick = (key: string): string | undefined => {
    const m = new RegExp(String.raw`^${key}:\s*(.+)$`, "m").exec(block);
    return m ? m[1].trim() : undefined;
  };
  return { name: pick("name"), description: pick("description") };
}

/** 列出 skills 根下的所有内置 Skill（含 frontmatter 元数据）。 */
export function listBundledSkills(skillsDir: string): BundledSkill[] {
  return readdirSync(skillsDir, { withFileTypes: true })
    .filter((e) => e.isDirectory() && existsSync(join(skillsDir, e.name, "SKILL.md")))
    .map((e) => {
      const dir = join(skillsDir, e.name);
      const meta = parseSkillMeta(readFileSync(join(dir, "SKILL.md"), "utf-8"));
      return {
        name: e.name,
        dir,
        title: meta.name ?? e.name,
        description: meta.description ?? "",
      };
    })
    .sort((a, b) => a.name.localeCompare(b.name));
}

export function skillsList(_ctx: CliContext): unknown {
  const skillsDir = resolveBundledSkillsDir();
  const skills = listBundledSkills(skillsDir);
  return {
    ok: true,
    skillsDir,
    count: skills.length,
    skills: skills.map((s) => ({
      name: s.name,
      title: s.title,
      description: s.description,
    })),
    hint: "用 `yoooclaw skills targets` 查看可安装目标，再用 `yoooclaw skills install --agent <agent>` 安装",
  };
}

interface InstallOpts {
  agent?: string;
  target?: string;
  copy?: boolean;
  force?: boolean;
}

export function skillsTargets(_ctx: CliContext): unknown {
  const targets = listSkillTargetCandidates();
  return {
    ok: true,
    targets,
    hint:
      "裸 `yoooclaw skills install` 只会在检测到唯一 Agent 时自动安装；否则请显式传 `--agent` 或 `--target`",
  };
}

interface InstallResult {
  name: string;
  status: "installed" | "skipped";
  /** symlink | copy；skipped 时为跳过原因 */
  via?: "symlink" | "copy";
  reason?: string;
  dest: string;
}

/** 现有目标是否已是指向同一来源的软链（幂等：重复安装不报错）。 */
function isSameSymlink(dest: string, source: string): boolean {
  try {
    if (!lstatSync(dest).isSymbolicLink()) return false;
    return realpathSync(dest) === realpathSync(source);
  } catch {
    return false;
  }
}

/** 把单个 Skill 落到 dest：软链或复制；返回安装/跳过结果。 */
function linkOrCopy(source: string, dest: string, mode: "symlink" | "copy"): void {
  if (mode === "copy") {
    cpSync(source, dest, { recursive: true });
    return;
  }
  try {
    symlinkSync(source, dest, "dir");
  } catch (err) {
    const e = err as NodeJS.ErrnoException;
    if (e.code === "EPERM" || e.code === "EACCES") {
      throw new YoooclawError(
        "YOOOCLAW_STORAGE_UNAVAILABLE",
        `创建软链失败（${e.code}）：${dest}`,
        { hint: "Windows 无管理员权限时无法创建软链，请改用 `yoooclaw skills install --copy`" },
      );
    }
    throw err;
  }
}

function installOne(
  skill: BundledSkill,
  target: string,
  mode: "symlink" | "copy",
  force: boolean,
): InstallResult {
  const source = realpathSync(skill.dir);
  const dest = join(target, skill.name);

  if (existsSync(dest) || isSymlinkPresent(dest)) {
    // 已是指向同一来源的软链 → 幂等视作已安装
    if (mode === "symlink" && isSameSymlink(dest, source)) {
      return { name: skill.name, status: "installed", via: "symlink", dest };
    }
    if (!force) {
      return { name: skill.name, status: "skipped", reason: "目标已存在，加 --force 覆盖", dest };
    }
    rmSync(dest, { recursive: true, force: true });
  }

  linkOrCopy(source, dest, mode);
  return { name: skill.name, status: "installed", via: mode, dest };
}

export function skillsInstall(
  _ctx: CliContext,
  _args: unknown[],
  opts: InstallOpts,
): unknown {
  const skillsDir = resolveBundledSkillsDir();
  const skills = listBundledSkills(skillsDir);
  const selection = resolveTargetSelection(opts);
  const target = selection.target;
  const mode: "symlink" | "copy" = opts.copy ? "copy" : "symlink";

  mkdirSync(target, { recursive: true });

  const results = skills.map((skill) => installOne(skill, target, mode, opts.force ?? false));
  const installed = results.filter((r) => r.status === "installed");
  return {
    ok: true,
    agent: selection.agent,
    agentLabel: selection.agentLabel,
    target,
    targetSource: selection.source,
    mode,
    sourceDir: skillsDir,
    installed: installed.map((r) => r.name),
    skipped: results.filter((r) => r.status === "skipped"),
    results,
    detectedTargets: selection.detectedTargets,
    hint:
      installed.length > 0
        ? "重启 agent 会话后即可被发现；试试说\"看看最近的通知\"触发 yoooclaw-notification-query"
        : "没有新安装的 Skill（已存在则加 --force 覆盖）",
  };
}

/** existsSync 对断链返回 false，这里补一个 lstat 探测，避免断链残留挡住安装。 */
function isSymlinkPresent(p: string): boolean {
  try {
    lstatSync(p);
    return true;
  } catch {
    return false;
  }
}

/**
 * 行为测试夹具 —— 把 CLI 当黑盒跑，断言 stdout JSON + exit code + 落盘。
 *
 * 两种执行方式：
 *   - runCli         进程内 run(argv)，spy process.stdout。快，适合 L2 纯本地命令与 L3 RPC 命令。
 *   - runCliSubprocess  spawn `bun src/bin.ts`，真进程隔离。适合 stdin（`-`）路径、daemon 生命周期。
 *
 * 所有用例都用 `YOOOCLAW_HOME` 指向 temp，绝不碰真实 ~/.yoooclaw / keychain。
 */
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { vi } from "vitest";
import { run } from "../../src/index.js";

const HERE = dirname(fileURLToPath(import.meta.url));
/** packages/cli/src/bin.ts —— 子进程入口（bun 可直接跑 TS 源码）。 */
export const CLI_BIN = join(HERE, "..", "..", "src", "bin.ts");

export interface CliResult {
  stdout: string;
  /** 最后一行 stdout 解析出的 JSON（命令统一输出体）。 */
  json: any;
  exitCode: number;
}

/** 注入 --format json（除非调用方已显式指定 --format）。 */
function withJsonFormat(args: string[]): string[] {
  return args.includes("--format") ? args : [...args, "--format", "json"];
}

function parseLastJson(stdout: string): any {
  const lines = stdout.trim().split("\n").filter(Boolean);
  const last = lines.at(-1);
  if (!last) return undefined;
  try {
    return JSON.parse(last);
  } catch {
    return undefined;
  }
}

export interface RunOpts {
  home: string;
  /** 额外环境变量；显式传 undefined 表示删除该变量（如清掉 YOOOCLAW_API_KEY）。 */
  env?: Record<string, string | undefined>;
}

/**
 * 进程内执行一条命令。注意：commander 解析失败（未知选项 / --help / --version）
 * 会调用 process.exit()，这类用例请改用 runCliSubprocess。
 */
export async function runCli(args: string[], opts: RunOpts): Promise<CliResult> {
  const chunks: string[] = [];
  const spy = vi
    .spyOn(process.stdout, "write")
    .mockImplementation((chunk: any) => {
      chunks.push(typeof chunk === "string" ? chunk : Buffer.from(chunk).toString("utf-8"));
      return true;
    });

  const saved = new Map<string, string | undefined>();
  const setEnv = (key: string, value: string | undefined): void => {
    if (!saved.has(key)) saved.set(key, process.env[key]);
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  };

  // 默认隔离：home 指向 temp，清掉可能干扰 api-key 解析的 env / profile。
  setEnv("YOOOCLAW_HOME", opts.home);
  setEnv("YOOOCLAW_API_KEY", undefined);
  setEnv("YOOOCLAW_PROFILE", undefined);
  for (const [key, value] of Object.entries(opts.env ?? {})) setEnv(key, value);

  const prevExit = process.exitCode;
  process.exitCode = 0;
  try {
    await run(["node", "yc", ...withJsonFormat(args)]);
  } finally {
    spy.mockRestore();
    for (const [key, value] of saved) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  const exitCode = typeof process.exitCode === "number" ? process.exitCode : 0;
  process.exitCode = prevExit;
  const stdout = chunks.join("");
  return { stdout, json: parseLastJson(stdout), exitCode };
}

export interface SubprocessOpts extends RunOpts {
  /** 传给被测进程 stdin（用于 `set-api-key -` / `--from-file -` / `--data -`）。 */
  input?: string;
}

/** spawn `bun src/bin.ts <args>`，真进程黑盒。 */
export function runCliSubprocess(args: string[], opts: SubprocessOpts): CliResult {
  const env: Record<string, string> = { ...process.env } as Record<string, string>;
  env.YOOOCLAW_HOME = opts.home;
  delete env.YOOOCLAW_API_KEY;
  delete env.YOOOCLAW_PROFILE;
  for (const [key, value] of Object.entries(opts.env ?? {})) {
    if (value === undefined) delete env[key];
    else env[key] = value;
  }
  const res = spawnSync("bun", [CLI_BIN, ...withJsonFormat(args)], {
    env,
    input: opts.input,
    encoding: "utf-8",
  });
  const stdout = res.stdout ?? "";
  return { stdout, json: parseLastJson(stdout), exitCode: res.status ?? 0 };
}

// ── temp home 管理 ──────────────────────────────────────────────

/** 建一个临时 YOOOCLAW_HOME（含 profiles/<profile> 目录）。 */
export function makeHome(profile = "default"): string {
  const home = mkdtempSync(join(tmpdir(), "yc-e2e-"));
  mkdirSync(join(home, "profiles", profile), { recursive: true });
  return home;
}

export function cleanHome(home: string): void {
  rmSync(home, { recursive: true, force: true });
}

export function profileDir(home: string, profile = "default"): string {
  return join(home, "profiles", profile);
}

// ── 数据 seed ───────────────────────────────────────────────────

export interface SeedNotification {
  appName: string;
  title: string;
  content: string;
  timestamp: string;
  appDisplayName?: string;
  senderName?: string;
  conversationType?: "group" | "private";
  conversationName?: string;
  clientLabel?: string;
}

/** 往 <profile>/notifications/<dateKey>.json 写一批通知。 */
export function seedNotifications(
  home: string,
  dateKey: string,
  items: SeedNotification[],
  profile = "default",
): void {
  const dir = join(profileDir(home, profile), "notifications");
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, `${dateKey}.json`), JSON.stringify(items), "utf-8");
}

/** 写 image index.json。 */
export function seedImages(home: string, images: unknown[], profile = "default"): void {
  const dir = join(profileDir(home, profile), "images");
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, "index.json"), JSON.stringify({ images }), "utf-8");
}

/** 写 recordings index.json。 */
export function seedRecordings(home: string, recordings: unknown[], profile = "default"): void {
  const dir = join(profileDir(home, profile), "recordings");
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, "index.json"), JSON.stringify({ recordings }), "utf-8");
}

/** 写 account 级共享 credentials.json（api-key）。 */
export function seedSharedCredentials(home: string, data: unknown): void {
  writeFileSync(join(home, "credentials.json"), JSON.stringify(data, null, 2), "utf-8");
}

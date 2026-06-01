/**
 * `~/.yoooclaw/` 目录布局解析（对齐 PRD「数据模型 / 目录布局」）。
 *
 * ~/.yoooclaw/
 *   credentials.json            account 级共享凭据（api-key），跨 profile 且与插件共享
 *   active-profile              当前 active profile 名（文本）
 *   profiles/<profile>/
 *     config.json
 *     credentials.json          instance 级密文（gateway token / webhook secret）
 *     daemon.lock / daemon.log
 *     notifications/ recordings/ images/ light-rules/ state/
 */
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export const DEFAULT_PROFILE = "default";

/** CLI 根数据目录。可用 `YOOOCLAW_HOME` 覆盖（测试 / 多实例隔离）。 */
export function rootDir(): string {
  return process.env.YOOOCLAW_HOME?.trim() || join(homedir(), ".yoooclaw");
}

/** account 级共享凭据文件（顶层，跨 profile + 插件共享）。 */
export function sharedCredentialsPath(): string {
  return join(rootDir(), "credentials.json");
}

/** 记录当前 active profile 的文本文件。 */
export function activeProfilePath(): string {
  return join(rootDir(), "active-profile");
}

/** 某个 profile 的目录。 */
export function profileDir(profile: string): string {
  return join(rootDir(), "profiles", profile);
}

export interface ProfilePaths {
  profile: string;
  dir: string;
  config: string;
  credentials: string;
  daemonLock: string;
  daemonLog: string;
  notifications: string;
  recordings: string;
  images: string;
  lightRules: string;
  state: string;
}

/** profiles/ 根目录。 */
export function profilesRoot(): string {
  return join(rootDir(), "profiles");
}

/** 列出已存在的 profile 名（profiles/ 下的目录）。 */
export function listProfileNames(): string[] {
  const root = profilesRoot();
  if (!existsSync(root)) return [];
  return readdirSync(root, { withFileTypes: true })
    .filter((e) => e.isDirectory())
    .map((e) => e.name)
    .sort((a, b) => a.localeCompare(b));
}

/** 读取 active-profile 文件内容（不存在返回 undefined）。 */
export function readActiveProfile(): string | undefined {
  const file = activeProfilePath();
  if (!existsSync(file)) return undefined;
  const name = readFileSync(file, "utf-8").trim();
  return name || undefined;
}

/** 解析某个 profile 下的全部关键路径。 */
export function profilePaths(profile: string): ProfilePaths {
  const dir = profileDir(profile);
  return {
    profile,
    dir,
    config: join(dir, "config.json"),
    credentials: join(dir, "credentials.json"),
    daemonLock: join(dir, "daemon.lock"),
    daemonLog: join(dir, "daemon.log"),
    notifications: join(dir, "notifications"),
    recordings: join(dir, "recordings"),
    images: join(dir, "images"),
    lightRules: join(dir, "light-rules"),
    state: join(dir, "state"),
  };
}

import { existsSync, readFileSync, readdirSync, mkdirSync } from "node:fs";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import type { StoredNotification } from "../notification/storage.js";

/**
 * Minimal Commander.js Command interface.
 * The real `Command` instance comes from the openclaw runtime at registration time;
 * we only declare the subset we use so that `commander` need not be a direct dep.
 */
export interface CliCommand {
  command(nameAndArgs: string): CliCommand;
  description(str: string): CliCommand;
  version(str: string, flags?: string, description?: string): CliCommand;
  option(flags: string, description: string, defaultValue?: string): CliCommand;
  requiredOption(flags: string, description: string): CliCommand;
  action(fn: (...args: any[]) => void | Promise<void>): CliCommand;
}

export interface CliContext {
  workspaceDir?: string;
  stateDir?: string;
}

/** Resolve the notifications storage directory (stateDir → workspace fallback) */
export function resolveNotificationsDir(ctx: CliContext): string | null {
  if (ctx.stateDir) {
    const dir = join(
      ctx.stateDir,
      "plugins",
      "phone-notifications",
      "notifications",
    );
    if (existsSync(dir)) return dir;
  }
  if (ctx.workspaceDir) {
    const dir = join(ctx.workspaceDir, "notifications");
    if (existsSync(dir)) return dir;
  }
  return null;
}

/** List available date keys (YYYY-MM-DD) sorted descending */
export function listDateKeys(dir: string): string[] {
  const pattern = /^(\d{4}-\d{2}-\d{2})\.json$/;
  const keys: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const m = pattern.exec(entry.name);
    if (m) keys.push(m[1]);
  }
  return keys.sort().reverse();
}

/** Read notifications for a specific date */
export function readDateFile(
  dir: string,
  dateKey: string,
): StoredNotification[] {
  const filePath = join(dir, `${dateKey}.json`);
  if (!existsSync(filePath)) return [];
  try {
    return JSON.parse(readFileSync(filePath, "utf-8"));
  } catch {
    return [];
  }
}

/** Format today as YYYY-MM-DD */
export function today(): string {
  return formatDate(new Date());
}

/** Format a Date as YYYY-MM-DD */
export function formatDate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

/** Get date N days ago as YYYY-MM-DD */
export function daysAgo(n: number): string {
  const d = new Date();
  d.setDate(d.getDate() - n);
  return formatDate(d);
}

/** Filter date keys to a [from, to] range (inclusive) */
export function filterDateRange(
  keys: string[],
  from: string,
  to: string,
): string[] {
  return keys.filter((k) => k >= from && k <= to);
}

/** Parse an ISO 8601 timestamp and exit with a CLI error on invalid input */
export function parseIsoTime(
  value: string,
  optionName: "--from" | "--to",
): number {
  const isoPattern =
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$/;
  if (!isoPattern.test(value)) {
    exitError(
      "INVALID_TIME",
      `${optionName} 必须是 ISO 8601 时间，例如 2026-03-01T09:00:00+08:00`,
    );
  }

  const ts = Date.parse(value);
  if (Number.isNaN(ts)) {
    exitError(
      "INVALID_TIME",
      `${optionName} 不是合法时间，例如 2026-03-01T09:00:00+08:00`,
    );
  }
  return ts;
}

/** Sort notifications by timestamp descending (newest first) */
export function sortNotificationsByTimestampDesc(
  items: StoredNotification[],
): StoredNotification[] {
  return items.sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
}

const APP_ALIAS_GROUPS = [
  ["微信", "wechat", "weixin", "com.tencent.mm", "com.tencent.xin"],
  ["企业微信", "wecom", "wxwork", "com.tencent.wework"],
  ["飞书", "feishu", "lark", "com.ss.android.lark", "com.bytedance.ee.lark", "com.larksuite.suite"],
  ["钉钉", "dingtalk", "com.alibaba.android.rimet"],
  ["qq", "腾讯qq", "com.tencent.mobileqq"],
];

function normalizeLookupText(value: string): string {
  return value.trim().toLowerCase();
}

function appAliasGroup(value: string): string[] | null {
  const normalized = normalizeLookupText(value);
  if (!normalized) return null;
  return APP_ALIAS_GROUPS.find((group) =>
    group.some((candidate) => normalizeLookupText(candidate) === normalized),
  ) ?? null;
}

export function matchesNotificationAppFilter(
  item: StoredNotification,
  app: string,
): boolean {
  const filter = normalizeLookupText(app);
  if (!filter) return true;

  const candidates = [item.appName, item.appDisplayName]
    .filter((value): value is string => typeof value === "string" && value.length > 0);
  if (candidates.some((candidate) => normalizeLookupText(candidate) === filter)) {
    return true;
  }

  const filterGroup = appAliasGroup(app);
  if (!filterGroup) return false;
  return candidates.some((candidate) => appAliasGroup(candidate) === filterGroup);
}

/** Async version of listDateKeys */
export async function listDateKeysAsync(dir: string): Promise<string[]> {
  const pattern = /^(\d{4}-\d{2}-\d{2})\.json$/;
  const keys: string[] = [];
  const entries = await readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    const m = pattern.exec(entry.name);
    if (m) keys.push(m[1]);
  }
  return keys.sort().reverse();
}

/** Async version of readDateFile */
export async function readDateFileAsync(
  dir: string,
  dateKey: string,
): Promise<StoredNotification[]> {
  const filePath = join(dir, `${dateKey}.json`);
  if (!existsSync(filePath)) return [];
  try {
    const content = await readFile(filePath, "utf-8");
    return JSON.parse(content);
  } catch {
    return [];
  }
}

/** Write progress message to stderr (visible to caller, won't pollute JSON stdout) */
export function progress(message: string): void {
  process.stderr.write(message + "\n");
}

/** Print JSON result to stdout */
export function output(data: unknown): void {
  process.stdout.write(JSON.stringify(data, null, 2) + "\n");
}

/** Print error JSON and exit with code 1 */
export function exitError(code: string, message: string): never {
  output({ ok: false, error: { code, message } });
  process.exit(1);
}

/** Resolve the recordings storage directory (stateDir → workspace fallback) */
export function resolveRecordingsDir(ctx: CliContext): string | null {
  if (ctx.stateDir) {
    const dir = join(
      ctx.stateDir,
      "plugins",
      "phone-notifications",
      "recordings",
    );
    if (existsSync(dir)) return dir;
  }
  if (ctx.workspaceDir) {
    const dir = join(ctx.workspaceDir, "recordings");
    if (existsSync(dir)) return dir;
  }
  return null;
}

/** Recording index entry (mirrors RecordingIndexEntry from storage.ts) */
export interface CliRecordingEntry {
  id: string;
  clientLabel?: string;
  metadata: {
    name: string;
    duration_sec: number;
    file_size_bytes: number;
    created_at: string;
    transfer_status: string;
    [key: string]: unknown;
  };
  status: string;
  audioFile?: string;
  srtFile?: string;
  transcriptDataFile?: string;
  transcriptFile?: string;
  summaryFile?: string;
  title?: string;
  lastError?: string;
  ingestedAt: string;
  updatedAt: string;
}

/** Resolve the asr-config.json path; creates parent dirs if needed */
export function resolveAsrConfigPath(ctx: CliContext): string {
  let base: string;
  if (ctx.stateDir) {
    base = join(ctx.stateDir, "plugins", "phone-notifications", "recordings");
  } else if (ctx.workspaceDir) {
    base = join(ctx.workspaceDir, "recordings");
  } else {
    exitError(
      "STORAGE_UNAVAILABLE",
      "无法确定录音存储目录：stateDir 和 workspaceDir 均未设置",
    );
  }
  mkdirSync(base, { recursive: true });
  return join(base, "asr-config.json");
}

/** Mask an API key, keeping first 4 and last 4 chars */
export function maskApiKey(key: string): string {
  if (key.length <= 8) return "***";
  return `${key.slice(0, 4)}***${key.slice(-4)}`;
}

/** Read recording index.json */
export function readRecordingIndex(
  dir: string,
): CliRecordingEntry[] {
  const indexPath = join(dir, "index.json");
  if (!existsSync(indexPath)) return [];
  try {
    const raw = JSON.parse(readFileSync(indexPath, "utf-8"));
    return Array.isArray(raw?.recordings) ? raw.recordings : [];
  } catch {
    return [];
  }
}

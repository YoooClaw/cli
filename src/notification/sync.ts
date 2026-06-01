/**
 * 通知同步 checkpoint 协议（供外部记忆系统按批拉取）。
 * 与 phone-notifications 插件版 `ntf-sync` 行为一致，错误改走 YoooclawError。
 */
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { writeJsonFile } from "../fs-utils.js";
import { YoooclawError } from "../errors.js";
import type { StoredNotification } from "../shared.js";

export const SYNC_FETCH_LIMIT = 300;

interface CheckpointData {
  [dateKey: string]: { lastIndex: number };
}

function checkpointPath(dir: string): string {
  return join(dir, ".checkpoint.json");
}

function readCheckpoint(dir: string): CheckpointData {
  const p = checkpointPath(dir);
  if (!existsSync(p)) return {};
  try {
    return JSON.parse(readFileSync(p, "utf-8")) as CheckpointData;
  } catch {
    return {};
  }
}

function writeCheckpoint(dir: string, data: CheckpointData): void {
  writeJsonFile(checkpointPath(dir), data);
}

function listDateKeys(dir: string): string[] {
  if (!existsSync(dir)) return [];
  const pattern = /^(\d{4}-\d{2}-\d{2})\.json$/;
  const keys: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const m = pattern.exec(entry.name);
    if (m) keys.push(m[1]);
  }
  return keys.sort((a, b) => b.localeCompare(a));
}

function readDateFile(dir: string, dateKey: string): StoredNotification[] {
  const filePath = join(dir, `${dateKey}.json`);
  if (!existsSync(filePath)) return [];
  try {
    const parsed = JSON.parse(readFileSync(filePath, "utf-8"));
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function parseIndexOption(raw: string, label: string): number {
  const n = Number.parseInt(raw, 10);
  if (!Number.isInteger(n) || String(n) !== raw || n < 0) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", `${label} 必须是非负整数`);
  }
  return n;
}

export function scanSync(dir: string): unknown {
  const checkpoint = readCheckpoint(dir);
  const pending: { date: string; count: number; startIndex: number }[] = [];
  let totalPending = 0;
  for (const dateKey of listDateKeys(dir)) {
    const items = readDateFile(dir, dateKey);
    const lastIndex = checkpoint[dateKey]?.lastIndex ?? -1;
    const unprocessed = items.length - (lastIndex + 1);
    if (unprocessed > 0) {
      pending.push({ date: dateKey, count: unprocessed, startIndex: lastIndex + 1 });
      totalPending += unprocessed;
    }
  }
  return { ok: true, pending, totalPending };
}

export function fetchSync(dir: string, date: string, maxEndIndexRaw?: string): unknown {
  const items = readDateFile(dir, date);
  if (items.length === 0) {
    throw new YoooclawError("YOOOCLAW_NOT_FOUND", `日期 ${date} 无通知数据`);
  }
  const checkpoint = readCheckpoint(dir);
  const lastIndex = checkpoint[date]?.lastIndex ?? -1;
  const startIndex = lastIndex + 1;
  let maxEndIndex = items.length - 1;
  if (maxEndIndexRaw) {
    maxEndIndex = Math.min(parseIndexOption(maxEndIndexRaw, "--max-end-index"), items.length - 1);
  }
  const snapshotEndExclusive = Math.max(startIndex, maxEndIndex + 1);
  const unprocessed = items.slice(startIndex, snapshotEndExclusive);
  const notifications = unprocessed.slice(0, SYNC_FETCH_LIMIT);
  const endIndex = notifications.length > 0 ? startIndex + notifications.length - 1 : lastIndex;
  const hasMore = unprocessed.length > notifications.length;
  return {
    ok: true,
    date,
    startIndex,
    endIndex,
    nextStartIndex: hasMore ? endIndex + 1 : null,
    limit: SYNC_FETCH_LIMIT,
    maxEndIndex,
    returned: notifications.length,
    totalUnprocessed: unprocessed.length,
    hasMore,
    notifications,
  };
}

export function commitSync(dir: string, date: string, endIndexRaw?: string): unknown {
  const items = readDateFile(dir, date);
  if (items.length === 0) {
    throw new YoooclawError("YOOOCLAW_NOT_FOUND", `日期 ${date} 无通知数据`);
  }
  const checkpoint = readCheckpoint(dir);
  const lastIndex = checkpoint[date]?.lastIndex ?? -1;
  let committedIndex: number;
  let commitMode: string;
  if (endIndexRaw) {
    committedIndex = parseIndexOption(endIndexRaw, "--end-index");
    if (committedIndex >= items.length) {
      throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--end-index 超出当前日期通知文件范围");
    }
    if (committedIndex < lastIndex) {
      throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--end-index 早于当前 checkpoint，不能回退消费进度");
    }
    if (committedIndex > lastIndex + SYNC_FETCH_LIMIT) {
      throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--end-index 超出单批 fetch 上限，不能跳过未处理通知");
    }
    commitMode = "exact-end-index";
  } else {
    committedIndex = Math.min(items.length - 1, lastIndex + SYNC_FETCH_LIMIT);
    commitMode = "legacy-batch-limit";
  }
  const hasMore = committedIndex < items.length - 1;
  checkpoint[date] = { lastIndex: committedIndex };
  writeCheckpoint(dir, checkpoint);
  return {
    ok: true,
    date,
    committedIndex,
    commitMode,
    limit: SYNC_FETCH_LIMIT,
    hasMore,
    nextStartIndex: hasMore ? committedIndex + 1 : null,
  };
}

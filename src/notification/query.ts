/**
 * 通知查询 —— 复用 phone-notifications 共享的匹配/收集逻辑，CLI 侧负责参数解析与错误。
 */
import { existsSync } from "node:fs";
import type { ProfilePaths } from "../paths.js";
import { YoooclawError } from "../errors.js";
import {
  collectMatchingNotifications,
  type NotificationQueryOptions,
  type StoredNotification,
} from "../shared.js";

const ISO_RE =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$/;

function parseIso(value: string, name: string): number {
  if (!ISO_RE.test(value)) {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      `${name} 必须是 ISO 8601 时间，例如 2026-03-01T09:00:00+08:00`,
    );
  }
  const ts = Date.parse(value);
  if (Number.isNaN(ts)) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", `${name} 不是合法时间`);
  }
  return ts;
}

function parseLimit(raw: string | undefined, fallback: number): number {
  if (raw === undefined) return fallback;
  if (!/^\d+$/.test(raw) || Number(raw) <= 0) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--limit 必须是大于 0 的整数");
  }
  return Number(raw);
}

export interface RawQueryOpts {
  from?: string;
  to?: string;
  app?: string;
  sender?: string;
  conversationType?: string;
  keyword?: string;
  client?: string;
  limit?: string;
}

export function buildQueryOptions(
  opts: RawQueryOpts,
  defaultLimit = 100,
): NotificationQueryOptions {
  if (
    opts.conversationType &&
    opts.conversationType !== "group" &&
    opts.conversationType !== "private"
  ) {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      "--conversation-type 只能是 group 或 private",
    );
  }
  const fromTs = opts.from ? parseIso(opts.from, "--from") : null;
  const toTs = opts.to ? parseIso(opts.to, "--to") : null;
  if (fromTs !== null && toTs !== null && fromTs > toTs) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--from 不能晚于 --to");
  }
  return {
    from: opts.from,
    to: opts.to,
    app: opts.app,
    sender: opts.sender,
    conversationType: opts.conversationType,
    keyword: opts.keyword,
    client: opts.client,
    limit: parseLimit(opts.limit, defaultLimit),
    fromTs,
    toTs,
    fromDateKey: opts.from ? opts.from.slice(0, 10) : null,
    toDateKey: opts.to ? opts.to.slice(0, 10) : null,
  };
}

/** 收集匹配的通知；通知目录不存在时返回空数组（无 daemon / 尚无数据是正常态）。 */
export async function queryNotifications(
  paths: ProfilePaths,
  options: NotificationQueryOptions,
): Promise<StoredNotification[]> {
  if (!existsSync(paths.notifications)) return [];
  return collectMatchingNotifications(paths.notifications, options);
}

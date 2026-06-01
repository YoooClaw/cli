import type { StoredNotification } from "../notification/storage.js";
import {
  exitError,
  listDateKeysAsync,
  parseIsoTime,
  readDateFileAsync,
  sortNotificationsByTimestampDesc,
  matchesNotificationAppFilter,
} from "./helpers.js";

export interface RawNotificationQueryOptions {
  from?: string;
  to?: string;
  app?: string;
  sender?: string;
  conversationType?: string;
  keyword?: string;
  limit?: string;
  client?: string;
}

export interface NotificationQueryOptions {
  from?: string;
  to?: string;
  app?: string;
  sender?: string;
  conversationType?: string;
  keyword?: string;
  client?: string;
  limit: number;
  fromTs: number | null;
  toTs: number | null;
  fromDateKey: string | null;
  toDateKey: string | null;
}

export function parsePositiveIntegerOption(
  rawValue: string | undefined,
  defaultValue: number,
  optionName: string,
  errorCode: string,
): number {
  const value = rawValue && rawValue.length > 0 ? rawValue : String(defaultValue);
  if (!/^\d+$/.test(value)) {
    exitError(errorCode, `${optionName} 必须是大于 0 的整数`);
  }

  const parsed = Number.parseInt(value, 10);
  if (parsed <= 0) {
    exitError(errorCode, `${optionName} 必须是大于 0 的整数`);
  }
  return parsed;
}

export function parseNotificationQueryOptions(
  opts: RawNotificationQueryOptions,
  defaultLimit = 100,
): NotificationQueryOptions {
  const limit = parsePositiveIntegerOption(
    opts.limit,
    defaultLimit,
    "--limit",
    "INVALID_LIMIT",
  );

  if (
    opts.conversationType
    && opts.conversationType !== "group"
    && opts.conversationType !== "private"
  ) {
    exitError(
      "INVALID_CONVERSATION_TYPE",
      "--conversation-type 只能是 group 或 private",
    );
  }

  const hasFrom = typeof opts.from === "string" && opts.from.length > 0;
  const hasTo = typeof opts.to === "string" && opts.to.length > 0;
  const fromTs = hasFrom ? parseIsoTime(opts.from!, "--from") : null;
  const toTs = hasTo ? parseIsoTime(opts.to!, "--to") : null;

  if (fromTs !== null && toTs !== null && fromTs > toTs) {
    exitError("INVALID_TIME_RANGE", "--from 不能晚于 --to");
  }

  return {
    from: opts.from,
    to: opts.to,
    app: opts.app,
    sender: opts.sender,
    conversationType: opts.conversationType,
    keyword: opts.keyword,
    client: opts.client,
    limit,
    fromTs,
    toTs,
    fromDateKey: hasFrom ? opts.from!.slice(0, 10) : null,
    toDateKey: hasTo ? opts.to!.slice(0, 10) : null,
  };
}

export function matchesNotificationQuery(
  item: StoredNotification,
  opts: NotificationQueryOptions,
): boolean {
  if (opts.client && opts.client !== "all") {
    const label = item.clientLabel ?? "legacy";
    if (label !== opts.client) return false;
  }
  if (opts.app && !matchesNotificationAppFilter(item, opts.app)) return false;
  if (
    opts.conversationType
    && item.conversationType !== opts.conversationType
  ) {
    return false;
  }

  const senderHaystack = [item.senderName, item.title]
    .filter((value): value is string => typeof value === "string" && value.length > 0)
    .join("\n");
  if (opts.sender && !senderHaystack.includes(opts.sender)) return false;

  const keywordHaystack = [
    item.title,
    item.content,
    item.senderName,
    item.conversationName,
  ]
    .filter((value): value is string => typeof value === "string" && value.length > 0)
    .join("\n");
  if (opts.keyword && !keywordHaystack.includes(opts.keyword)) return false;

  const itemTs = Date.parse(item.timestamp);
  if (Number.isNaN(itemTs)) return false;
  if (opts.fromTs !== null && itemTs < opts.fromTs) return false;
  if (opts.toTs !== null && itemTs > opts.toTs) return false;

  return true;
}

export async function collectMatchingNotifications(
  dir: string,
  opts: NotificationQueryOptions,
): Promise<StoredNotification[]> {
  const keys = await listDateKeysAsync(dir);
  const results: StoredNotification[] = [];
  const canStopAfterLimit = opts.fromTs === null && opts.toTs === null;

  for (const dateKey of keys) {
    if (opts.fromDateKey && dateKey < opts.fromDateKey) continue;
    if (opts.toDateKey && dateKey > opts.toDateKey) continue;

    const items = sortNotificationsByTimestampDesc([
      ...(await readDateFileAsync(dir, dateKey)),
    ]);
    for (const item of items) {
      if (!matchesNotificationQuery(item, opts)) continue;
      results.push(item);
      if (canStopAfterLimit && results.length >= opts.limit) {
        return results;
      }
    }
  }

  return sortNotificationsByTimestampDesc(results).slice(0, opts.limit);
}

/**
 * notification service —— search / summary / stats / storage-path + shortcuts。
 * 全部纯读磁盘（🟢），不需要 daemon。
 */
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { buildQueryOptions, queryNotifications, type RawQueryOpts } from "../notification/query.js";
import type { NotificationQueryOptions } from "../shared.js";
import { daysAgo, today, formatDate } from "../shared.js";
import type { StoredNotification } from "../shared.js";

const MAX_LIMIT = Number.MAX_SAFE_INTEGER;

export async function notificationSearch(
  ctx: CliContext,
  _args: unknown[],
  opts: RawQueryOpts,
): Promise<unknown> {
  const options = buildQueryOptions(opts);
  return queryNotifications(ctx.paths, options);
}

interface SummaryOpts extends RawQueryOpts {
  sample?: string;
  top?: string;
}

function topCounts(
  items: StoredNotification[],
  pick: (n: StoredNotification) => string | undefined,
  topN: number,
): Array<{ key: string; count: number }> {
  const counts = new Map<string, number>();
  for (const item of items) {
    const key = pick(item);
    if (!key) continue;
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([key, count]) => ({ key, count }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key))
    .slice(0, topN);
}

export async function notificationSummary(
  ctx: CliContext,
  _args: unknown[],
  opts: SummaryOpts,
): Promise<unknown> {
  const sample = opts.sample ? Number(opts.sample) : 30;
  const top = opts.top ? Number(opts.top) : 10;
  const options = buildQueryOptions({ ...opts, limit: String(MAX_LIMIT) }, MAX_LIMIT);
  const items = await queryNotifications(ctx.paths, options);
  return {
    ok: true,
    total: items.length,
    range: { from: opts.from ?? null, to: opts.to ?? null },
    topApps: topCounts(items, (n) => n.appDisplayName || n.appName, top),
    topSenders: topCounts(items, (n) => n.senderName || n.title, top),
    sample: items.slice(0, sample),
  };
}

interface StatsOpts {
  from?: string;
  to?: string;
  app?: string;
  sender?: string;
  client?: string;
  dim?: string;
}

const HOUR_KEY = (n: StoredNotification): string =>
  String(new Date(n.timestamp).getHours()).padStart(2, "0");

const DATE_ONLY_RE = /^\d{4}-\d{2}-\d{2}$/;
const ISO_TIME_RE =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$/;

interface StatsBoundary {
  raw: string;
  exactTs: number | null;
  startTs: number;
  endTs: number;
  minDateKey: string;
  maxDateKey: string;
}

interface StatsRange {
  from: string;
  to: string;
  fromTs: number | null;
  toTs: number | null;
  fromDateKey: string;
  toDateKey: string;
}

function parseDateParts(value: string): { year: number; month: number; day: number } | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return null;
  const year = Number(m[1]);
  const month = Number(m[2]);
  const day = Number(m[3]);
  const d = new Date(year, month - 1, day);
  if (
    d.getFullYear() !== year ||
    d.getMonth() !== month - 1 ||
    d.getDate() !== day
  ) {
    return null;
  }
  return { year, month, day };
}

function localDateTimestamp(
  parts: { year: number; month: number; day: number },
  endOfDay: boolean,
): number {
  return new Date(
    parts.year,
    parts.month - 1,
    parts.day,
    endOfDay ? 23 : 0,
    endOfDay ? 59 : 0,
    endOfDay ? 59 : 0,
    endOfDay ? 999 : 0,
  ).getTime();
}

function minDateKey(...keys: string[]): string {
  return keys.sort()[0];
}

function maxDateKey(...keys: string[]): string {
  return keys.sort().at(-1)!;
}

function parseStatsBoundary(value: string, optionName: "--from" | "--to"): StatsBoundary {
  if (DATE_ONLY_RE.test(value)) {
    const parts = parseDateParts(value);
    if (!parts) {
      throw new YoooclawError(
        "YOOOCLAW_INVALID_ARGUMENT",
        `${optionName} 必须是合法日期 YYYY-MM-DD`,
      );
    }
    const startTs = localDateTimestamp(parts, false);
    const endTs = localDateTimestamp(parts, true);
    return {
      raw: value,
      exactTs: null,
      startTs,
      endTs,
      minDateKey: value,
      maxDateKey: value,
    };
  }

  if (!ISO_TIME_RE.test(value)) {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      `${optionName} 必须是 YYYY-MM-DD 或 ISO 8601 时间，例如 2026-06-02 或 2026-06-02T09:00:00+08:00`,
    );
  }
  const exactTs = Date.parse(value);
  if (Number.isNaN(exactTs)) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", `${optionName} 不是合法时间`);
  }

  const declaredDateKey = value.slice(0, 10);
  const localDateKey = formatDate(new Date(exactTs));
  return {
    raw: value,
    exactTs,
    startTs: exactTs,
    endTs: exactTs,
    minDateKey: minDateKey(declaredDateKey, localDateKey),
    maxDateKey: maxDateKey(declaredDateKey, localDateKey),
  };
}

function buildStatsRange(fromRaw: string | undefined, toRaw: string | undefined): StatsRange {
  const from = parseStatsBoundary(fromRaw ?? daysAgo(7), "--from");
  const to = parseStatsBoundary(toRaw ?? today(), "--to");
  if (from.startTs > to.endTs) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--from 不能晚于 --to");
  }
  return {
    from: from.raw,
    to: to.raw,
    fromTs: from.exactTs,
    toTs: to.exactTs,
    fromDateKey: from.minDateKey,
    toDateKey: to.maxDateKey,
  };
}

export async function notificationStats(
  ctx: CliContext,
  _args: unknown[],
  opts: StatsOpts,
): Promise<unknown> {
  const range = buildStatsRange(opts.from, opts.to);
  const dim = opts.dim ?? "all";
  const allowed = ["date", "app", "sender", "hour", "client", "all"];
  if (!allowed.includes(dim)) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", `--dim 只能是 ${allowed.join("|")}`);
  }
  const options: NotificationQueryOptions = {
    from: range.from,
    to: range.to,
    app: opts.app,
    sender: opts.sender,
    client: opts.client,
    limit: MAX_LIMIT,
    fromTs: range.fromTs,
    toTs: range.toTs,
    fromDateKey: range.fromDateKey,
    toDateKey: range.toDateKey,
  };
  const items = await queryNotifications(ctx.paths, options);

  const byDate = topCounts(items, (n) => formatDate(new Date(n.timestamp)), MAX_LIMIT);
  const byApp = topCounts(items, (n) => n.appDisplayName || n.appName, MAX_LIMIT);
  const bySender = topCounts(items, (n) => n.senderName || n.title, MAX_LIMIT);
  const byHour = topCounts(items, HOUR_KEY, MAX_LIMIT);
  const byClient = topCounts(items, (n) => n.clientLabel ?? "legacy", MAX_LIMIT);

  const dims = { date: byDate, app: byApp, sender: bySender, hour: byHour, client: byClient };
  return {
    ok: true,
    total: items.length,
    range: { from: range.from, to: range.to },
    dim,
    ...(dim === "all" ? dims : { [dim]: dims[dim as keyof typeof dims] }),
  };
}

export function notificationStoragePath(ctx: CliContext): unknown {
  return { ok: true, path: ctx.paths.notifications };
}

export async function notificationToday(
  ctx: CliContext,
  _args: unknown[],
  opts: { client?: string } = {},
): Promise<unknown> {
  const day = today();
  const options = buildQueryOptions({
    from: `${day}T00:00:00${tzOffset()}`,
    to: `${day}T23:59:59${tzOffset()}`,
    client: opts.client,
    limit: String(MAX_LIMIT),
  }, MAX_LIMIT);
  return queryNotifications(ctx.paths, options);
}

export async function notificationRecent(
  ctx: CliContext,
  _args: unknown[],
  opts: { client?: string } = {},
): Promise<unknown> {
  const fromTs = Date.now() - 60 * 60 * 1000;
  const options: NotificationQueryOptions = {
    limit: MAX_LIMIT,
    client: opts.client,
    fromTs,
    toTs: null,
    fromDateKey: formatDate(new Date(fromTs)),
    toDateKey: null,
  };
  return queryNotifications(ctx.paths, options);
}

export function notificationUnread(): unknown {
  throw new YoooclawError(
    "YOOOCLAW_NOT_IMPLEMENTED",
    "+unread 预留：需要先落地通知的已读状态模型",
  );
}

/** 本地时区偏移，如 +08:00。 */
function tzOffset(): string {
  const min = -new Date().getTimezoneOffset();
  const sign = min >= 0 ? "+" : "-";
  const abs = Math.abs(min);
  const hh = String(Math.floor(abs / 60)).padStart(2, "0");
  const mm = String(abs % 60).padStart(2, "0");
  return `${sign}${hh}:${mm}`;
}

/**
 * log service —— `log [keyword]` + `+errors`（🟢，纯读 daemon 日志文件）。
 */
import type { CliContext } from "../context.js";
import { searchLogs } from "../log/reader.js";
import { daysAgo, today } from "../shared.js";

interface LogOpts {
  from?: string;
  to?: string;
  limit?: string;
  level?: string;
}

export function logSearch(
  ctx: CliContext,
  args: unknown[],
  opts: LogOpts,
): unknown {
  const [keyword] = args as [string | undefined];
  const lines = searchLogs(ctx.paths.daemonLog, {
    keyword,
    level: opts.level,
    from: opts.from ?? daysAgo(7),
    to: opts.to ?? today(),
    limit: opts.limit ? Number(opts.limit) : 50,
  });
  return { ok: true, keyword: keyword ?? null, total: lines.length, lines };
}

export function logErrors(ctx: CliContext): unknown {
  const lines = searchLogs(ctx.paths.daemonLog, {
    level: "error",
    from: daysAgo(1),
    to: today(),
    limit: 50,
  });
  return { ok: true, level: "error", total: lines.length, lines };
}

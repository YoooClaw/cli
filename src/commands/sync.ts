/**
 * sync service —— scan / fetch / commit（通知同步给记忆系统）。
 */
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { scanSync, fetchSync, commitSync } from "../notification/sync.js";

export function syncScan(ctx: CliContext): unknown {
  return scanSync(ctx.paths.notifications);
}

export function syncFetch(
  ctx: CliContext,
  _args: unknown[],
  opts: { date?: string; maxEndIndex?: string },
): unknown {
  if (!opts.date) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--date 必填（YYYY-MM-DD）");
  }
  return fetchSync(ctx.paths.notifications, opts.date, opts.maxEndIndex);
}

export function syncCommit(
  ctx: CliContext,
  _args: unknown[],
  opts: { date?: string; endIndex?: string },
): unknown {
  if (!opts.date) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--date 必填（YYYY-MM-DD）");
  }
  return commitSync(ctx.paths.notifications, opts.date, opts.endIndex);
}

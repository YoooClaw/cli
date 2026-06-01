/**
 * image service —— list / status / path / storage-path + +latest（🟢，纯读磁盘）。
 */
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import {
  readImageIndex,
  resolveImageFile,
  type ImageIndexEntry,
} from "../image/storage.js";
import { matchesNotificationAppFilter } from "../shared.js";

interface ListOpts {
  status?: string;
  app?: string;
  client?: string;
  from?: string;
  to?: string;
  limit?: string;
}

function byCreatedDesc(a: ImageIndexEntry, b: ImageIndexEntry): number {
  return Date.parse(b.metadata.created_at) - Date.parse(a.metadata.created_at);
}

export function imageList(
  ctx: CliContext,
  _args: unknown[],
  opts: ListOpts,
): unknown {
  let items = readImageIndex(ctx.paths);
  if (opts.status) items = items.filter((i) => i.status === opts.status);
  if (opts.client && opts.client !== "all") {
    items = items.filter((i) => (i.clientLabel ?? "legacy") === opts.client);
  }
  if (opts.app) {
    const app = opts.app;
    items = items.filter((i) =>
      matchesNotificationAppFilter(
        { appName: i.metadata.source_app ?? "", title: "", content: "", timestamp: "" },
        app,
      ),
    );
  }
  if (opts.from) {
    const fromTs = Date.parse(opts.from);
    items = items.filter((i) => Date.parse(i.metadata.created_at) >= fromTs);
  }
  if (opts.to) {
    const toTs = Date.parse(opts.to);
    items = items.filter((i) => Date.parse(i.metadata.created_at) <= toTs);
  }
  items = items.sort(byCreatedDesc);
  const limit = opts.limit ? Number(opts.limit) : 100;
  return { ok: true, total: items.length, images: items.slice(0, limit) };
}

export function imageStatus(ctx: CliContext, args: unknown[]): unknown {
  const [id] = args as [string];
  const entry = readImageIndex(ctx.paths).find((i) => i.imageId === id);
  if (!entry) throw new YoooclawError("YOOOCLAW_NOT_FOUND", `图片不存在：${id}`);
  return { ok: true, image: entry };
}

export function imagePath(
  ctx: CliContext,
  args: unknown[],
  opts: { thumbnail?: boolean },
): unknown {
  const [id] = args as [string];
  const entry = readImageIndex(ctx.paths).find((i) => i.imageId === id);
  if (!entry) throw new YoooclawError("YOOOCLAW_NOT_FOUND", `图片不存在：${id}`);

  const relative = opts.thumbnail ? entry.thumbnail : entry.localFile;
  if (entry.status !== "synced" || !relative) {
    throw new YoooclawError(
      "YOOOCLAW_IMAGE_NOT_READY",
      `图片 ${id} 尚未下载完成`,
      { status: entry.status, lastError: entry.lastError ?? null },
    );
  }
  return { ok: true, path: resolveImageFile(ctx.paths, relative) };
}

export function imageStoragePath(ctx: CliContext): unknown {
  return { ok: true, path: ctx.paths.images };
}

export function imageLatest(ctx: CliContext): unknown {
  const items = readImageIndex(ctx.paths).sort(byCreatedDesc);
  return { ok: true, image: items[0] ?? null };
}

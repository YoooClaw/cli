/**
 * 图片下发通道 —— 与录音链路同构：先落元数据 + respond(true)，再后台从 OSS 下载本体。
 *
 * 入口：daemon 的 HTTP `POST /images` 与 gateway `images.sync` 都调用 ingestImage。
 * 复刻录音的同步状态机：syncing → synced / sync_failed；imageId 幂等去重。
 */
import { createWriteStream, existsSync, readFileSync } from "node:fs";
import { Readable } from "node:stream";
import { join } from "node:path";
import { ensureDir, writeJsonFile } from "../fs-utils.js";
import type { ProfilePaths } from "../paths.js";
import { currentClientLabel } from "../shared.js";
import {
  imagesIndexPath,
  type ImageIndexEntry,
  type ImageMetadata,
} from "./storage.js";

export interface ImageSyncPayload {
  imageId: string;
  image: ImageMetadata;
}

interface ChannelLogger {
  info: (m: string) => void;
  warn: (m: string) => void;
}

function readAll(paths: ProfilePaths): ImageIndexEntry[] {
  const p = imagesIndexPath(paths);
  if (!existsSync(p)) return [];
  try {
    const raw = JSON.parse(readFileSync(p, "utf-8"));
    return Array.isArray(raw?.images) ? raw.images : [];
  } catch {
    return [];
  }
}

function writeAll(paths: ProfilePaths, images: ImageIndexEntry[]): void {
  ensureDir(paths.images);
  writeJsonFile(imagesIndexPath(paths), { images });
}

function upsert(paths: ProfilePaths, entry: ImageIndexEntry): void {
  const images = readAll(paths);
  const idx = images.findIndex((i) => i.imageId === entry.imageId);
  if (idx >= 0) images[idx] = entry;
  else images.push(entry);
  writeAll(paths, images);
}

function extFromMime(mime?: string): string {
  if (!mime) return "jpg";
  if (mime.includes("png")) return "png";
  if (mime.includes("webp")) return "webp";
  if (mime.includes("gif")) return "gif";
  return "jpg";
}

export interface IngestImageResult {
  ok: boolean;
  imageId: string;
  status: ImageIndexEntry["status"];
  deduped?: boolean;
}

/**
 * 接收图片下发：校验必填 → 落 syncing 元数据 → 后台下载（fire-and-forget）。
 * 幂等：imageId 已存在且 oss_image_url 未变化则跳过重复下载。
 */
export function ingestImage(
  paths: ProfilePaths,
  payload: ImageSyncPayload,
  opts: { maxBytes: number; logger: ChannelLogger },
): IngestImageResult {
  const { imageId, image } = payload;
  if (!imageId || !image?.oss_image_url || !image?.created_at) {
    throw new Error("imageId / image.oss_image_url / image.created_at 必填");
  }

  const existing = readAll(paths).find((i) => i.imageId === imageId);
  if (existing && existing.metadata.oss_image_url === image.oss_image_url && existing.status === "synced") {
    return { ok: true, imageId, status: "synced", deduped: true };
  }

  const entry: ImageIndexEntry = {
    imageId,
    clientLabel: currentClientLabel("default"),
    metadata: image,
    localFile: null,
    thumbnail: null,
    status: "syncing",
    lastError: null,
    syncedAt: null,
  };
  upsert(paths, entry);

  void downloadInBackground(paths, entry, opts).catch((err) => {
    opts.logger.warn(`image[${imageId}] 后台下载异常：${(err as Error).message}`);
  });

  return { ok: true, imageId, status: "syncing" };
}

async function downloadInBackground(
  paths: ProfilePaths,
  entry: ImageIndexEntry,
  opts: { maxBytes: number; logger: ChannelLogger },
): Promise<void> {
  const filesDir = join(paths.images, "files");
  ensureDir(filesDir);
  const relative = `files/${entry.imageId}.${extFromMime(entry.metadata.mime_type)}`;
  const dest = join(paths.images, relative);

  try {
    const res = await fetch(entry.metadata.oss_image_url);
    if (!res.ok || !res.body) {
      throw new Error(`OSS 返回 ${res.status}`);
    }
    const declared = Number(res.headers.get("content-length") ?? "0");
    if (declared && declared > opts.maxBytes) {
      throw new Error(`图片超过上限 ${opts.maxBytes} 字节`);
    }

    let written = 0;
    const fileStream = createWriteStream(dest);
    const reader = Readable.fromWeb(res.body as never);
    for await (const chunk of reader) {
      written += (chunk as Buffer).length;
      if (written > opts.maxBytes) {
        fileStream.destroy();
        throw new Error(`图片超过上限 ${opts.maxBytes} 字节`);
      }
      fileStream.write(chunk);
    }
    await new Promise<void>((resolve, reject) => {
      fileStream.end((err?: Error | null) => (err ? reject(err) : resolve()));
    });

    upsert(paths, {
      ...entry,
      localFile: relative,
      status: "synced",
      lastError: null,
      syncedAt: new Date().toISOString(),
    });
    opts.logger.info(`image[${entry.imageId}] 下载完成 (${written} bytes)`);
  } catch (err) {
    upsert(paths, {
      ...entry,
      status: "sync_failed",
      lastError: (err as Error).message,
    });
    opts.logger.warn(`image[${entry.imageId}] 下载失败：${(err as Error).message}`);
  }
}

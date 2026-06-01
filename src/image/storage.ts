/**
 * 图片本地存储读取（images/index.json）。
 *
 * 元数据与同步状态机由 daemon 的图片通道写入（见 PRD「图片通道」）；
 * CLI 的 image 查询命令纯读这里，不需要 daemon 在跑。
 */
import { existsSync, readFileSync } from "node:fs";
import { isAbsolute, join } from "node:path";
import type { ProfilePaths } from "../paths.js";

export type ImageSyncStatus = "syncing" | "synced" | "sync_failed";

export interface ImageMetadata {
  oss_image_url: string;
  created_at: string;
  mime_type?: string;
  width?: number;
  height?: number;
  size_bytes?: number;
  source_app?: string;
  caption?: string;
}

export interface ImageIndexEntry {
  imageId: string;
  /** 来源客户端 label；旧数据无该字段，查询时视为 legacy */
  clientLabel?: string;
  metadata: ImageMetadata;
  /** 相对 images/ 的本地路径，如 files/img_xxx.jpg。 */
  localFile?: string | null;
  thumbnail?: string | null;
  status: ImageSyncStatus;
  lastError?: string | null;
  syncedAt?: string | null;
}

export function imagesIndexPath(paths: ProfilePaths): string {
  return join(paths.images, "index.json");
}

/** 读取图片索引；目录/文件不存在返回空数组。 */
export function readImageIndex(paths: ProfilePaths): ImageIndexEntry[] {
  const indexPath = imagesIndexPath(paths);
  if (!existsSync(indexPath)) return [];
  try {
    const raw = JSON.parse(readFileSync(indexPath, "utf-8"));
    return Array.isArray(raw?.images) ? raw.images : [];
  } catch {
    return [];
  }
}

/** 把索引里的相对路径解析为绝对路径。 */
export function resolveImageFile(
  paths: ProfilePaths,
  relative: string,
): string {
  return isAbsolute(relative) ? relative : join(paths.images, relative);
}

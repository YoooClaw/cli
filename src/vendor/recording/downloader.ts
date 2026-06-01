/**
 * OSS 文件下载器（约束 R-6）
 *
 * 从 OSS URL 下载原始音频（mp3）和打点文件（srt）到本地。
 * 支持重试、超时控制、下载进度日志。
 */

import { createWriteStream, existsSync, mkdirSync, statSync, unlinkSync } from "node:fs";
import { dirname } from "node:path";
import { pipeline } from "node:stream/promises";
import { Readable } from "node:stream";
import type { Logger } from "../logger.js";

// ─── Types ───

export interface DownloadOptions {
  /** 下载超时（毫秒），默认 5 分钟 */
  timeoutMs?: number;
  /** 最大重试次数，默认 3 */
  maxRetries?: number;
  /** 重试间隔基数（毫秒），默认 2000 */
  retryBackoffMs?: number;
}

export interface DownloadResult {
  ok: boolean;
  /** 下载的文件大小（字节） */
  sizeBytes?: number;
  /** 耗时（毫秒） */
  elapsedMs?: number;
  /** 错误信息 */
  error?: string;
}

const DEFAULT_TIMEOUT_MS = 5 * 60 * 1000; // 5 分钟（最大 ~56MB 文件）
const DEFAULT_MAX_RETRIES = 3;
const DEFAULT_RETRY_BACKOFF_MS = 2000;

// ─── Public API ───

/**
 * 从 URL 下载文件到本地路径
 */
export async function downloadFile(
  url: string,
  destPath: string,
  logger: Logger,
  options?: DownloadOptions,
): Promise<DownloadResult> {
  const timeoutMs = options?.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const maxRetries = options?.maxRetries ?? DEFAULT_MAX_RETRIES;
  const retryBackoffMs = options?.retryBackoffMs ?? DEFAULT_RETRY_BACKOFF_MS;

  mkdirSync(dirname(destPath), { recursive: true });

  let lastError: string | undefined;

  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    const startMs = Date.now();
    try {
      logger.info(
        `[downloader] 开始下载 (attempt ${attempt}/${maxRetries}): ${url}`,
      );

      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), timeoutMs);

      try {
        const res = await fetch(url, { signal: controller.signal });

        if (!res.ok) {
          throw new Error(`HTTP ${res.status} ${res.statusText}`);
        }

        if (!res.body) {
          throw new Error("响应体为空");
        }

        // 写入文件
        const writeStream = createWriteStream(destPath);
        const readable = Readable.fromWeb(res.body as any);
        await pipeline(readable, writeStream);

        const elapsed = Date.now() - startMs;
        const fileSize = existsSync(destPath)
          ? statSync(destPath).size
          : 0;

        logger.info(
          `[downloader] 下载完成: ${destPath} (${formatBytes(fileSize)}, ${elapsed}ms)`,
        );

        return { ok: true, sizeBytes: fileSize, elapsedMs: elapsed };
      } finally {
        clearTimeout(timer);
      }
    } catch (err: any) {
      lastError = err?.message ?? String(err);

      // 清理可能写了一半的文件
      try {
        if (existsSync(destPath)) unlinkSync(destPath);
      } catch {
        // ignore
      }

      const isAbort = err?.name === "AbortError";
      logger.warn(
        `[downloader] 下载失败 (attempt ${attempt}/${maxRetries}): ${isAbort ? "超时" : lastError}`,
      );

      if (attempt < maxRetries) {
        const delay = retryBackoffMs * Math.pow(2, attempt - 1);
        logger.info(`[downloader] ${delay}ms 后重试...`);
        await sleep(delay);
      }
    }
  }

  return { ok: false, error: lastError ?? "下载失败" };
}

/**
 * 下载录音的音频文件和打点文件
 */
export async function downloadRecordingFiles(
  ossAudioUrl: string,
  ossSrtUrl: string | undefined,
  audioDestPath: string,
  srtDestPath: string,
  logger: Logger,
  options?: DownloadOptions,
): Promise<{
  audio: DownloadResult;
  srt: DownloadResult | null;
}> {
  // 下载音频（必须）
  const audio = await downloadFile(ossAudioUrl, audioDestPath, logger, options);

  // 下载打点文件（可选）
  let srt: DownloadResult | null = null;
  if (ossSrtUrl) {
    srt = await downloadFile(ossSrtUrl, srtDestPath, logger, options);
    if (!srt.ok) {
      // 打点文件下载失败不阻断主流程，仅警告
      logger.warn(`[downloader] 打点文件下载失败（非致命）: ${srt.error}`);
    }
  }

  return { audio, srt };
}

// ─── Helpers ───

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)}MB`;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

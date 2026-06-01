/**
 * Account Service OSS 文件删除（imp-account-user-controllers-api §5.7）
 *
 * 调用 `POST /account/file/delete?fileUrl=...`，`fileUrl` 通过查询串传递
 * （后端确认的传参方式，form-urlencoded / multipart 经网关都会丢字段）。
 */

import type { Logger } from "../logger.js";
import { loadApiKey } from "../auth/credentials.js";
import { getEnvUrls } from "../env.js";

interface AccountServiceResponse {
  code?: string | number;
  msg?: string | null;
  data?: unknown;
}

export interface DeleteOssFileResult {
  ok: boolean;
  error?: string;
}

export interface DeleteAccountOssFileOptions {
  /** 最大尝试次数（含首次），默认 3 */
  maxAttempts?: number;
  /** 指数退避基数（毫秒），默认 500 */
  backoffMs?: number;
}

type AttemptOutcome =
  | { kind: "ok" }
  | { kind: "permanent"; error: string }
  | { kind: "retryable"; error: string };

const DEFAULT_DELETE_MAX_ATTEMPTS = 3;
const DEFAULT_DELETE_BACKOFF_MS = 500;

function getDeleteMaxAttempts(): number {
  const raw = process.env.OPENCLAW_OSS_DELETE_MAX_ATTEMPTS;
  if (raw) {
    const parsed = Number(raw);
    if (Number.isFinite(parsed) && parsed >= 1) return Math.floor(parsed);
  }
  return DEFAULT_DELETE_MAX_ATTEMPTS;
}

function getDeleteBackoffMs(): number {
  const raw = process.env.OPENCLAW_OSS_DELETE_BACKOFF_MS;
  if (raw) {
    const parsed = Number(raw);
    if (Number.isFinite(parsed) && parsed >= 0) return parsed;
  }
  return DEFAULT_DELETE_BACKOFF_MS;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// 仅对网络抖动/服务端临时不可用做重试；4xx（除 429）是确定性失败，重试无意义。
function isRetryableStatus(status: number): boolean {
  return status === 429 || status >= 500;
}

function parseBody(text: string): AccountServiceResponse | "invalid-json" | undefined {
  if (!text) return undefined;
  try {
    return JSON.parse(text) as AccountServiceResponse;
  } catch {
    return "invalid-json";
  }
}

function extractCode(payload: AccountServiceResponse | undefined): string | undefined {
  if (!payload) return undefined;
  if (typeof payload.code === "number") return String(payload.code);
  return payload.code?.trim?.();
}

async function attemptDelete(
  endpoint: string,
  headerKey: string,
  logger: Logger,
): Promise<AttemptOutcome> {
  let res: Response;
  try {
    res = await fetch(endpoint, {
      method: "POST",
      headers: { "X-Api-Key-Id": headerKey },
    });
  } catch (err: any) {
    return { kind: "retryable", error: err?.message ?? String(err) };
  }

  const text = await res.text();

  if (!res.ok) {
    const error = `HTTP ${res.status} ${text.slice(0, 200)}`;
    if (isRetryableStatus(res.status)) {
      return { kind: "retryable", error };
    }
    logger.warn(
      `[account-oss-delete] HTTP 错误: status=${res.status}, body=${text.slice(0, 300)}`,
    );
    return { kind: "permanent", error };
  }

  const payload = parseBody(text);
  if (payload === "invalid-json") {
    logger.warn(`[account-oss-delete] 响应非 JSON: body=${text.slice(0, 300)}`);
    return { kind: "permanent", error: "response is not JSON" };
  }

  const code = extractCode(payload);
  if (code !== "000000") {
    const msg = payload?.msg?.trim?.() || text.slice(0, 200);
    logger.warn(`[account-oss-delete] 业务失败: code=${code ?? "n/a"}, msg=${msg}`);
    return { kind: "permanent", error: `${code ?? "unknown"} ${msg}`.trim() };
  }

  return { kind: "ok" };
}

/**
 * 删除 account-service 上传到 OSS 的文件。
 * 只接受形如 `https://{bucket}.{endpoint}/{objectKey}` 的完整 URL（即上传接口返回值）。
 * 失败不抛异常，由调用方按场景决定是否致命。
 *
 * 重试策略：网络异常 / HTTP 5xx / 429 走指数退避；其余（4xx、非 JSON、业务 code）直接返回。
 */
export async function deleteAccountOssFile(
  fileUrl: string,
  logger: Logger,
  options?: DeleteAccountOssFileOptions,
): Promise<DeleteOssFileResult> {
  const trimmed = fileUrl?.trim();
  if (!trimmed) {
    return { ok: false, error: "fileUrl is empty" };
  }

  const apiKey = loadApiKey();
  if (!apiKey) {
    return { ok: false, error: "API Key 未设置，跳过 OSS 文件删除" };
  }

  const baseEndpoint = getEnvUrls().accountFileDeleteUrl;
  const headerKey = apiKey.startsWith("Bearer ")
    ? apiKey.slice("Bearer ".length)
    : apiKey;

  const url = new URL(baseEndpoint);
  url.searchParams.set("fileUrl", trimmed);
  const endpoint = url.toString();

  const maxAttempts = Math.max(
    1,
    Math.floor(options?.maxAttempts ?? getDeleteMaxAttempts()),
  );
  const baseBackoff = Math.max(0, options?.backoffMs ?? getDeleteBackoffMs());

  logger.info(
    `[account-oss-delete] 提交 OSS 文件删除: endpoint=${endpoint}, maxAttempts=${maxAttempts}`,
  );

  let lastError = "unknown error";

  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    const outcome = await attemptDelete(endpoint, headerKey, logger);

    if (outcome.kind === "ok") {
      logger.info(`[account-oss-delete] OSS 文件已删除: fileUrl=${trimmed}`);
      return { ok: true };
    }

    if (outcome.kind === "permanent") {
      return { ok: false, error: outcome.error };
    }

    lastError = outcome.error;
    if (attempt < maxAttempts) {
      const delay = baseBackoff * Math.pow(2, attempt - 1);
      logger.warn(
        `[account-oss-delete] 临时失败 (attempt ${attempt}/${maxAttempts}): ${outcome.error}, ${delay}ms 后重试`,
      );
      await sleep(delay);
    } else {
      logger.warn(
        `[account-oss-delete] 临时失败 (attempt ${attempt}/${maxAttempts}, 放弃): ${outcome.error}`,
      );
    }
  }

  return { ok: false, error: lastError };
}

/**
 * daemon 录音桥 —— CLI 独立形态下的 recordings.sync 实现。
 *
 * 为什么不直接用插件 registerRecordingInterfaces 里注册的 sync：
 *   - 插件版 sync 要求 caller（手机端）每次把 asr 配置随 params 一起带上来；
 *   - CLI 形态下 ASR 配置由 `yc recording setup-asr` 一次性写到 ~/.yoooclaw/<p>/recordings/asr-config.json，
 *     手机端不一定会带 asr —— 没带就要 fallback 读本地配置；
 *   - 同时 mode=api 时若用户没填 apiKey，要回退到 account 级 ock- key（resolveApiKey）。
 *
 * 用法：daemon main.ts 先调 registerRecordingInterfaces 装上 list/status/rename/... 一类查询/管理方法，
 * 再调本模块 `overrideRecordingSync` 覆盖 sync / retranscribe（runtime.gatewayMethods 是 Map，后写覆盖）。
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { IncomingMessage, ServerResponse } from "node:http";
import {
  canStartTranscription,
  handleRecordingSync,
  RecordingStorage,
  triggerTranscription,
  validateAsrConfig,
  type AsrConfig,
  type RecordingMetadata,
  type RecordingStatusEvent,
} from "../shared.js";
import type { StandaloneRuntime } from "./runtime.js";
import type { DaemonLogger } from "./logger.js";

export interface RecordingSyncDeps {
  runtime: StandaloneRuntime;
  storage: RecordingStorage;
  logger: DaemonLogger;
  /** account 级 ock- key（mode=api 且未指定 apiKey 时回退） */
  fallbackApiKey?: string | (() => string | undefined);
  /** asr-config.json 所在目录（= ctx.paths.recordings） */
  asrConfigDir: string;
  /** 状态机变化时的回调（写日志 + 落 events.jsonl） */
  notifyStatus: (event: RecordingStatusEvent) => void;
}

interface SyncParams {
  recordingId?: unknown;
  recording?: RecordingMetadata;
  asr?: AsrConfig;
}

interface RetranscribeParams {
  recordingId?: unknown;
  asr?: AsrConfig;
}

const ASR_CONFIG_FILE = "asr-config.json";

function trimToString(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed || undefined;
}

/** 从 asr-config.json 读 AsrConfig；不存在 / 损坏返回 undefined。 */
export function loadLocalAsrConfig(asrConfigDir: string): AsrConfig | undefined {
  const path = join(asrConfigDir, ASR_CONFIG_FILE);
  if (!existsSync(path)) return undefined;
  try {
    const raw = JSON.parse(readFileSync(path, "utf-8")) as AsrConfig;
    return raw;
  } catch {
    return undefined;
  }
}

/**
 * 合并 caller 传入的 asr 与本地 fallback：
 * - 优先用 caller 的；
 * - 没传则用本地；
 * - mode=api 且最终 apiKey 仍为空时，用 account 级 fallback。
 */
export function resolveAsrConfig(
  callerAsr: AsrConfig | undefined,
  localAsr: AsrConfig | undefined,
  fallbackApiKey?: string,
): AsrConfig | undefined {
  const chosen = callerAsr ?? localAsr;
  if (!chosen) return undefined;
  if (chosen.mode !== "api") return chosen;
  const apiKey = chosen.api?.apiKey ?? fallbackApiKey;
  if (!apiKey) return chosen;
  return { ...chosen, api: { ...(chosen.api ?? {}), apiKey } };
}

/**
 * 进入「稳定/终态」即认为一轮 sync 流程已结束，可解除 in-flight 标记并清理 lastError 残留。
 * - synced：sync 阶段成功（如果没有 ASR 也就停在这里）
 * - transcribed：ASR 成功（终态）
 * - transcribe_failed / sync_failed：失败终态
 */
const TERMINAL_STATUSES = new Set<string>([
  "synced",
  "transcribed",
  "transcribe_failed",
  "sync_failed",
]);
/** 仅这两个算「最终成功态」—— 终态时若 lastError 还残留是无用噪音，清掉。 */
const TERMINAL_SUCCESS_STATUSES = new Set<string>(["synced", "transcribed"]);
/** 单条 in-flight 的兜底超时：5 分钟内若没收到终态事件，强制释放（避免句柄泄漏）。 */
const INFLIGHT_TIMEOUT_MS = 5 * 60 * 1000;

/** 覆盖注册 recordings.sync / recordings.retranscribe gateway + POST /recordings HTTP。 */
export function overrideRecordingSync(deps: RecordingSyncDeps): void {
  const { runtime, storage, logger, fallbackApiKey, asrConfigDir, notifyStatus } = deps;
  const readFallbackApiKey = (): string | undefined =>
    typeof fallbackApiKey === "function" ? fallbackApiKey() : fallbackApiKey;

  /**
   * 同一 recordingId 在 sync→ASR 全流程中只允许一次 background 跑：
   * 手机端在 OSS 上传过程中常常会以秒级间隔重推同一条 sync，phone-notifications 的 handler 不去重，
   * 两条并行的 runRecordingSyncInBackground 会撞状态机（transcribing → synced 非法）。
   * daemon 层加 in-flight 标记后，第二条直接 short-circuit，仅返回当前 storage 状态。
   */
  const inFlight = new Map<string, NodeJS.Timeout>();

  function resolveConfiguredAsr(callerAsr: AsrConfig | undefined): AsrConfig | undefined {
    return resolveAsrConfig(
      callerAsr,
      loadLocalAsrConfig(asrConfigDir),
      readFallbackApiKey(),
    );
  }

  function markInFlight(recordingId: string): void {
    clearTimeout(inFlight.get(recordingId));
    inFlight.set(
      recordingId,
      setTimeout(() => inFlight.delete(recordingId), INFLIGHT_TIMEOUT_MS),
    );
  }

  function releaseInFlight(recordingId: string): void {
    const t = inFlight.get(recordingId);
    if (t) {
      clearTimeout(t);
      inFlight.delete(recordingId);
    }
  }

  const wrappedNotifyStatus = (event: RecordingStatusEvent): void => {
    notifyStatus(event);
    if (!TERMINAL_STATUSES.has(event.transfer_status)) return;
    releaseInFlight(event.recordingId);
    // 终态成功（synced / transcribed）+ storage lastError 还在 → 清理。
    // 注意：不能用 event.error 做条件，因为 event.error 正是从 storage.lastError 拼出来的，
    // 互为因果。直接看 storage 现状即可。
    if (TERMINAL_SUCCESS_STATUSES.has(event.transfer_status)) {
      const entry = storage.findById(event.recordingId);
      if (entry?.lastError) {
        try {
          storage.setLastError(event.recordingId);
          logger.info(`[recording-sync] 终态 ${event.transfer_status} 清理 lastError 残留: ${event.recordingId}`);
        } catch (err) {
          logger.warn(`[recording-sync] 清理 lastError 失败: ${(err as Error).message}`);
        }
      }
    }
  };

  async function doSync(params: SyncParams): Promise<
    | { ok: true; data: Awaited<ReturnType<typeof handleRecordingSync>> }
    | { ok: false; code: string; message: string }
  > {
    const recordingId = trimToString(params.recordingId);
    if (!recordingId) {
      return { ok: false, code: "INVALID_PARAMS", message: "recordingId is required" };
    }
    const recording = params.recording;
    if (!recording || !recording.oss_audio_url || !recording.created_at) {
      return {
        ok: false,
        code: "INVALID_PARAMS",
        message: "recording with oss_audio_url and created_at is required",
      };
    }
    if (params.asr) {
      const err = validateAsrConfig(params.asr);
      if (err) return { ok: false, code: "INVALID_PARAMS", message: err };
    }

    // in-flight 去重：手机重推时直接 short-circuit，返回当前 storage 状态
    if (inFlight.has(recordingId)) {
      const entry = storage.findById(recordingId);
      logger.info(
        `[recording-sync] in-flight 跳过重复 sync: ${recordingId}（当前 ${entry?.status ?? "unknown"}）`,
      );
      return {
        ok: true,
        data: {
          ok: true,
          recordingId,
          transfer_status: entry?.status ?? "syncing_openclaw",
        },
      };
    }

    const asr = resolveConfiguredAsr(params.asr);
    if (asr && !params.asr) {
      logger.info(
        `[recording-sync] 使用本地 asr-config.json（mode=${asr.mode}${
          asr.mode === "api" && asr.api?.apiKey ? ", key=ock-***" : ""
        }）`,
      );
    }

    markInFlight(recordingId);
    const result = await handleRecordingSync(
      recordingId,
      recording,
      storage,
      asr,
      logger,
      { notifyStatus: wrappedNotifyStatus },
    );
    return { ok: true, data: result };
  }

  function retranscribe(params: RetranscribeParams): (
    | { ok: true; data: { ok: true; recordingId: string; message: string } }
    | { ok: false; code: string; message: string }
  ) {
    const recordingId = trimToString(params.recordingId);
    if (!recordingId) {
      return { ok: false, code: "INVALID_PARAMS", message: "recordingId is required" };
    }

    if (params.asr) {
      const err = validateAsrConfig(params.asr);
      if (err) return { ok: false, code: "ASR_NOT_CONFIGURED", message: err };
    }

    const asr = resolveConfiguredAsr(params.asr);
    const asrError = validateAsrConfig(asr);
    if (asrError) {
      return { ok: false, code: "ASR_NOT_CONFIGURED", message: asrError };
    }

    const entry = storage.findById(recordingId);
    if (!entry) {
      return { ok: false, code: "NOT_FOUND", message: `Recording not found: ${recordingId}` };
    }

    if (!canStartTranscription(entry.status)) {
      return {
        ok: false,
        code: "INVALID_STATE",
        message: `Recording status does not allow retranscribe: ${entry.status}`,
      };
    }

    if (asr && !params.asr) {
      logger.info(
        `[recording-retranscribe] 使用本地 asr-config.json（mode=${asr.mode}${
          asr.mode === "api" && asr.api?.apiKey ? ", key=ock-***" : ""
        }）`,
      );
    }

    triggerTranscription(recordingId, storage, asr!, logger, {
      notifyStatus: wrappedNotifyStatus,
    }).catch((err) => {
      logger.error(
        `[recording-retranscribe] 重试转写失败: ${recordingId}, ${err?.message ?? err}`,
      );
    });

    return {
      ok: true,
      data: { ok: true, recordingId, message: "转写已重新触发" },
    };
  }

  runtime.registerGatewayMethod("recordings.sync", async ({ params, respond }) => {
    const outcome = await doSync((params ?? {}) as SyncParams);
    if (outcome.ok) {
      respond(outcome.data.ok, outcome.data, outcome.data.ok ? undefined : {
        code: "SYNC_FAILED",
        message: outcome.data.error ?? "Unknown error",
      });
    } else {
      respond(false, null, { code: outcome.code, message: outcome.message });
    }
  });

  runtime.registerGatewayMethod("recordings.retranscribe", ({ params, respond }) => {
    const outcome = retranscribe((params ?? {}) as RetranscribeParams);
    if (outcome.ok) {
      respond(true, outcome.data);
    } else {
      respond(false, null, { code: outcome.code, message: outcome.message });
    }
  });

  runtime.registerHttpRoute({
    path: "/recordings",
    auth: "gateway",
    async handler(req: IncomingMessage, res: ServerResponse) {
      if (req.method !== "POST") {
        res.writeHead(405, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Method Not Allowed" }));
        return;
      }
      const body = await readJsonBody(req);
      if (body === undefined) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Invalid JSON" }));
        return;
      }
      const outcome = await doSync(body as SyncParams);
      if (outcome.ok) {
        res.writeHead(outcome.data.ok ? 200 : 500, { "Content-Type": "application/json" });
        res.end(JSON.stringify(outcome.data));
      } else {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: outcome.message }));
      }
    },
  });

  logger.info("daemon 已覆盖 recordings.sync / recordings.retranscribe + POST /recordings（注入本地 ASR 配置）");
}

async function readJsonBody(req: IncomingMessage): Promise<unknown | undefined> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  const raw = Buffer.concat(chunks).toString("utf-8");
  if (!raw) return {};
  try {
    return JSON.parse(raw);
  } catch {
    return undefined;
  }
}

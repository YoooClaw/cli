/**
 * ASR 转写调度器（§6 / §7）
 *
 * 按 arc-long-recording §6.2 定义的两层配置结构调度 ASR：
 * - mode = "api"：YoooClaw 托管云端 ASR（经 model-proxy 长录音接口调用）
 * - mode = "local"：已停用（保留枚举值用于兼容旧客户端，调用时直接返回错误）
 * - mode = "yoooclaw"：YoooClaw 自建 ASR（P2，预留接口）
 *
 * 转写流程：
 * 1. 读取录音 OSS URL 与本地文件
 * 2. 调用云端 model-proxy 获取转写文本
 * 3. 将关键点以 [关键点 MM:SS] 内嵌转写文本
 * 4. 自动生成摘要标题（≤ 10 字）
 * 5. 写入 /recordings/transcripts/日期_时间_摘要.md
 */

import { existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type {
  AsrConfig,
  AsrApiConfig,
  RecordingMarker,
  RecordingAsrInitResult,
  RecordingTranscriptItem,
} from "../types.js";
import type { Logger } from "../logger.js";
import { requireApiKey, loadApiKey } from "../auth/credentials.js";
import { getEnvUrls } from "../env.js";
import {
  buildTranscriptDataFilename,
  buildTranscriptDocument,
  extractSourceTextListFromDocument,
} from "./transcript-document.js";

const DEFAULT_LONG_RECORDING_POLL_INTERVAL_MS = 2000;
const DEFAULT_LONG_RECORDING_MAX_POLL_ATTEMPTS = 3600;
const LONG_RECORDING_RUNNING_STATUSES = new Set(["PENDING", "RUNNING", "SUSPENDED"]);
const LONG_RECORDING_TERMINAL_FAILURE_STATUSES = new Set(["FAILED", "CANCELED", "UNKNOWN"]);

const DEFAULT_HTTP_MAX_ATTEMPTS = 3;
const DEFAULT_HTTP_RETRY_BACKOFF_MS = 2000;

// ─── Types ───

export interface TranscriptionResult {
  ok: boolean;
  /** 转写全文 */
  text?: string;
  /** 带时间戳的分段（如 ASR 支持） */
  segments?: TranscriptSegment[];
  /** 自动生成的摘要标题 */
  summary?: string;
  /** 给客户端展示的摘要文本 */
  summaryText?: string;
  /** 分类信息（如会议纪要、采访） */
  category?: string;
  /** 转写来源信息 */
  sourceInfo?: {
    provider: string;
    taskId?: string;
    requestId?: string;
    status?: string;
  };
  /** Provider 原始响应，供后续兼容字段演进 */
  rawResponse?: unknown;
  /** 错误信息 */
  error?: string;
}

export interface TranscriptSegment {
  /** 开始时间（毫秒） */
  start_ms: number;
  /** 结束时间（毫秒） */
  end_ms: number;
  /** 文本内容 */
  text: string;
  /** 说话人 ID（如云端 ASR 返回） */
  speaker_id?: number;
}

interface ModelProxyLongRecordingSubmitRequest {
  audioOssUrl: string;
  language?: string;
  enableNormalization?: boolean;
}

interface ModelProxyLongRecordingResult {
  sourceText?: string | null;
  sourceTextList?: ModelProxyLongRecordingSourceTextItem[] | null;
  summaryResult?: string | null;
  category?: string | null;
  title?: string | null;
}

interface ModelProxyLongRecordingSourceTextItem {
  content?: string | null;
  speakerId?: number | null;
  startTime?: number | null;
  endTime?: number | null;
}

interface ModelProxyLongRecordingStatusResponse {
  taskId?: string;
  status?: string;
  requestId?: string;
  errorMessage?: string | null;
  recordResult?: ModelProxyLongRecordingResult | null;
}

interface ResponseEnvelope<T> {
  success?: boolean;
  code?: number | string;
  message?: string | null;
  e?: unknown;
  data?: T | null;
}

// ─── Public API ───

/**
 * 判断 ASR 是否已配置
 */
export function isAsrConfigured(config?: AsrConfig): boolean {
  return !!config && !validateAsrConfig(config);
}

/**
 * 校验客户端下发的 ASR 配置。
 * 返回 undefined 表示配置可用；返回字符串表示错误原因。
 */
export function validateAsrConfig(config?: AsrConfig): string | undefined {
  if (!config?.mode) {
    return "asr.mode is required";
  }

  switch (config.mode) {
    case "api":
      return undefined;
    case "local":
      return "本地 Whisper 模式已停用，请改用 asr.mode = \"api\"";
    case "yoooclaw":
      return "YoooClaw ASR 尚未实现（P2）";
    default:
      return `未知的 ASR mode: ${(config as { mode: string }).mode}`;
  }
}

/**
 * 显式初始化 ASR 能力。
 * - mode=api：校验插件本地 API Key 是否存在，并返回最终使用的 model-proxy submit-task endpoint
 * - mode=local：按配置预下载模型，并返回本地环境状态
 */
export async function initializeAsr(
  config: AsrConfig,
  dataDir: string,
  logger: Logger,
): Promise<RecordingAsrInitResult> {
  const validationError = validateAsrConfig(config);
  if (validationError) {
    return {
      ok: false,
      mode: config.mode,
      error: validationError,
    };
  }

  switch (config.mode) {
    case "api": {
      const endpoint = resolveModelProxyLongRecordingSubmitEndpoint(config.api);
      const keyConfigured = hasText(config.api?.apiKey) || hasText(loadApiKey());

      if (!keyConfigured) {
        return {
          ok: false,
          mode: "api",
          provider: "model-proxy",
          endpoint,
          language: config.api?.language ?? "auto",
          keyConfigured: false,
          error:
            "API Key 未设置，请在本次 asr.api.apiKey 中传入，或先写入 credentials.json / 执行 ntf auth set-api-key <apiKey>",
        };
      }

      return {
        ok: true,
        mode: "api",
        provider: "model-proxy",
        endpoint,
        language: config.api?.language ?? "auto",
        keyConfigured: true,
      };
    }
    case "local":
      return {
        ok: false,
        mode: "local",
        error: "本地 Whisper 模式已停用，请改用 asr.mode = \"api\"",
      };
    case "yoooclaw":
      return {
        ok: false,
        mode: "yoooclaw",
        error: "YoooClaw ASR 尚未实现（P2）",
      };
    default:
      return {
        ok: false,
        mode: config.mode,
        error: `未知的 ASR mode: ${config.mode}`,
      };
  }
}

/**
 * 执行 ASR 转写
 *
 * @param audioFilePath 本地音频文件绝对路径
 * @param config ASR 配置（两层结构：mode + api/local）
 * @param logger 日志
 * @param options 额外上下文（云端 ASR 需要 OSS URL）
 */
export async function transcribeAudio(
  audioFilePath: string,
  config: AsrConfig,
  logger: Logger,
  options: {
    audioOssUrl?: string;
    audioDurationMs?: number;
  } = {},
): Promise<TranscriptionResult> {
  if (!existsSync(audioFilePath)) {
    return { ok: false, error: `音频文件不存在: ${audioFilePath}` };
  }

  logger.info(
    `[asr] 开始转写: mode=${config.mode}, file=${audioFilePath}`,
  );

  try {
    switch (config.mode) {
      case "api":
        return await transcribeWithModelProxy(
          options.audioOssUrl,
          options.audioDurationMs,
          config.api,
          logger,
        );
      case "local":
        return {
          ok: false,
          error: "本地 Whisper 模式已停用，请改用 asr.mode = \"api\"",
        };
      case "yoooclaw":
        return { ok: false, error: "YoooClaw ASR 尚未实现（P2）" };
      default:
        return {
          ok: false,
          error: `未知的 ASR mode: ${config.mode}`,
        };
    }
  } catch (err: any) {
    const msg = err?.message ?? String(err);
    logger.error(`[asr] 转写异常: ${msg}`);
    return { ok: false, error: msg };
  }
}

/**
 * 将转写结果与关键点标记合并，生成最终 Markdown 转写文本
 *
 * @param result 转写结果
 * @param markers 关键点列表
 * @param recordingName 录音名称
 * @param durationSec 录音总时长（秒）
 * @param createdAt 录音创建时间
 */
export function buildTranscriptMarkdown(
  result: TranscriptionResult,
  markers: RecordingMarker[],
  recordingName: string,
  durationSec: number,
  createdAt: string,
): string { 
  const summary = result.summary ?? recordingName.slice(0, 10);
  const lines: string[] = [];

  // 元数据头
  lines.push(`# ${summary}`);
  lines.push("");
  lines.push(`> 录音名称：${recordingName}`);
  lines.push(`> 时长：${formatDuration(durationSec)}`);
  lines.push(`> 创建时间：${createdAt}`);
  if (markers.length > 0) {
    lines.push(`> 关键点数：${markers.length}`);
  }
  lines.push("");
  lines.push("---");
  lines.push("");

  if (result.segments && result.segments.length > 0) {
    // 有分段的转写文本：在合适的位置插入关键点标记
    const sortedMarkers = [...markers].sort(
      (a, b) => a.timestamp_ms - b.timestamp_ms,
    );
    let markerIdx = 0;

    for (const seg of result.segments) {
      // 在当前段之前插入所有对应的关键点
      while (
        markerIdx < sortedMarkers.length &&
        sortedMarkers[markerIdx].timestamp_ms <= seg.start_ms
      ) {
        const m = sortedMarkers[markerIdx];
        lines.push(
          `**[关键点 ${formatTimestamp(m.timestamp_ms)}]**`,
        );
        lines.push("");
        markerIdx++;
      }

      const segmentText = formatTranscriptSegmentText(seg);
      if (segmentText) {
        lines.push(segmentText);
        lines.push("");
      }
    }

    // 追加剩余关键点
    while (markerIdx < sortedMarkers.length) {
      const m = sortedMarkers[markerIdx];
      lines.push(
        `**[关键点 ${formatTimestamp(m.timestamp_ms)}]**`,
      );
      lines.push("");
      markerIdx++;
    }
  } else if (result.text) {
    // 纯文本转写：在文本头部集中列出关键点
    if (markers.length > 0) {
      lines.push("### 关键点");
      lines.push("");
      for (const m of markers) {
        lines.push(`- **[关键点 ${formatTimestamp(m.timestamp_ms)}]**`);
      }
      lines.push("");
      lines.push("---");
      lines.push("");
    }
    lines.push(result.text);
    lines.push("");
  }

  return lines.join("\n");
}

/**
 * 从转写文本中提取摘要标题（≤ 10 字）
 */
export function extractSummary(text: string): string {
  // 取第一句话，截断到 10 字
  const firstLine = text.split(/[。！？\n]/).find((s) => s.trim().length > 0);
  if (!firstLine) return "录音转写";
  const trimmed = firstLine.trim();
  return trimmed.length <= 10 ? trimmed : trimmed.slice(0, 10);
}

/**
 * 完整的转写工作流：转写 → 生成 Markdown → 写入文件
 */
export async function runTranscriptionWorkflow(params: {
  audioFilePath: string;
  audioOssUrl?: string;
  config: AsrConfig;
  markers: RecordingMarker[];
  recordingName: string;
  durationSec: number;
  createdAt: string;
  transcriptsDir: string;
  transcriptDataDir: string;
  summariesDir: string;
  recordingId: string;
  logger: Logger;
}): Promise<{
  ok: boolean;
  transcriptFilename?: string;
  transcriptDataFilename?: string;
  summaryFilename?: string;
  transcript?: RecordingTranscriptItem[];
  summary?: string;
  title?: string;
  error?: string;
}> {
  const {
    audioFilePath,
    audioOssUrl,
    config,
    markers,
    recordingName,
    durationSec,
    createdAt,
    transcriptsDir,
    transcriptDataDir,
    summariesDir,
    recordingId,
    logger,
  } = params;

  // 1. ASR 转写
  const result = await transcribeAudio(audioFilePath, config, logger, {
    audioOssUrl,
    audioDurationMs: Math.max(0, Math.round(durationSec * 1000)),
  });
  if (!result.ok) {
    return { ok: false, error: result.error };
  }

  // 2. 提取摘要
  const title = normalizeOptionalText(result.summary)
    ? normalizeOptionalText(result.summary)!
    : extractSummary(result.text ?? "");
  const summary = result.summaryText ?? "";
  result.summary = title;
  const transcriptData = buildTranscriptDocument({
    recordingId,
    generatedAt: new Date().toISOString(),
    source: result.sourceInfo ?? {
      provider: config.mode === "api" ? "model-proxy" : config.mode,
    },
    title,
    category: result.category,
    summary,
    text: result.text,
    segments: (result.segments ?? []).map((segment) => ({
      text: segment.text,
      startMs: segment.start_ms,
      endMs: segment.end_ms,
      speakerId: segment.speaker_id,
    })),
    raw: result.rawResponse,
  });

  // 3. 生成 Markdown
  const markdown = buildTranscriptMarkdown(
    result,
    markers,
    recordingName,
    durationSec,
    createdAt,
  );

  // 4. 写入文件
  const transcriptDataFilename = buildTranscriptDataFilename(recordingId);
  const transcriptDataPath = join(transcriptDataDir, transcriptDataFilename);
  writeFileSync(
    transcriptDataPath,
    JSON.stringify(transcriptData, null, 2),
    "utf-8",
  );
  logger.info(`[asr] 转写 JSON 已写入: ${transcriptDataPath}`);

  const safeSummary = title
    .replace(/[/\\:*?"<>|]/g, "")
    .trim()
    .slice(0, 20);
  const filename = safeSummary
    ? `${recordingId}_${safeSummary}.md`
    : `${recordingId}.md`;
  const filePath = join(transcriptsDir, filename);
  writeFileSync(filePath, markdown, "utf-8");
  logger.info(`[asr] 转写文本已写入: ${filePath}`);

  let summaryFilename: string | undefined;
  if (summary) {
    summaryFilename = `${recordingId}.md`;
    const summaryFilePath = join(summariesDir, summaryFilename);
    writeFileSync(summaryFilePath, summary, "utf-8");
    logger.info(`[asr] 摘要文本已写入: ${summaryFilePath}`);
  }

  return {
    ok: true,
    transcriptFilename: filename,
    transcriptDataFilename,
    summaryFilename,
    transcript: extractSourceTextListFromDocument(transcriptData),
    summary,
    title,
  };
}

// ─── ASR Provider Implementations ───

/**
 * 通过 model-proxy 的长录音接口执行云端转写。
 */
async function transcribeWithModelProxy(
  audioOssUrl: string | undefined,
  audioDurationMs: number | undefined,
  apiConfig: AsrApiConfig | undefined,
  logger: Logger,
): Promise<TranscriptionResult> {
  const normalizedAudioOssUrl = normalizeOptionalText(audioOssUrl);
  if (!normalizedAudioOssUrl) {
    return { ok: false, error: "API 模式缺少 audioOssUrl，无法调用 model-proxy" };
  }

  const apiKey = resolveModelProxyApiKey(apiConfig);
  const submitEndpoint = resolveModelProxyLongRecordingSubmitEndpoint(apiConfig);
  const submitBody = buildLongRecordingSubmitRequest(normalizedAudioOssUrl, apiConfig);

  logger.info(
    `[asr-submit] 提交长录音任务: endpoint=${submitEndpoint}, body=${stringifyForLog(submitBody) ?? "{}"}`,
  );

  let res: Response;
  try {
    res = await fetchWithRetry(
      submitEndpoint,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Api-Key-Id": apiKey,
        },
        body: JSON.stringify(submitBody),
      },
      { logger, context: "asr-submit" },
    );
  } catch (err: any) {
    const msg = err?.message ?? String(err);
    logger.error(`[asr-submit] 提交长录音任务网络异常 (已重试): ${msg}`);
    return {
      ok: false,
      error: `Model Proxy ASR submit network error: ${msg}`,
    };
  }

  if (!res.ok) {
    const errText = await res.text();
    logger.error(
      `[asr-submit-response] 提交长录音任务失败: status=${res.status}, body=${errText.slice(0, 500)}`,
    );
    return {
      ok: false,
      error: `Model Proxy ASR error: ${res.status} ${errText.slice(0, 200)}`,
    };
  }

  const raw = (await res.json()) as
    | ModelProxyLongRecordingStatusResponse
    | ResponseEnvelope<ModelProxyLongRecordingStatusResponse>;
  logger.info(
    `[asr-submit-response] 提交长录音任务响应: ${stringifyForLog(raw) ?? "{}"}`,
  );
  const submitEnvelopeError = buildModelProxyEnvelopeError(raw);
  if (submitEnvelopeError) {
    logger.error(
      `[asr-submit-response] 提交长录音任务失败: ${submitEnvelopeError}`,
    );
    return {
      ok: false,
      error: `Model Proxy ASR 提交失败: ${submitEnvelopeError}`,
    };
  }

  const data = unwrapResponse(raw);
  const taskId = normalizeOptionalText(data?.taskId);
  const requestId = normalizeOptionalText(data?.requestId);
  const status = normalizeLongRecordingStatus(data?.status);

  if (!taskId) {
    return {
      ok: false,
      error: "Model Proxy ASR 响应缺少 taskId",
    };
  }

  logger.info(
    `[asr] Model Proxy 长录音任务已提交: taskId=${taskId}, status=${status ?? "UNKNOWN"}, requestId=${requestId ?? "n/a"}`,
  );

  if (status && LONG_RECORDING_TERMINAL_FAILURE_STATUSES.has(status)) {
    return {
      ok: false,
      error: buildLongRecordingStatusError(data, status),
    };
  }

  return await pollLongRecordingTaskResult({
    apiKey,
    taskId,
    initialRequestId: requestId,
    audioDurationMs,
    apiConfig,
    logger,
  });
}

// ─── Helpers ───

function resolveModelProxyLongRecordingSubmitEndpoint(apiConfig?: AsrApiConfig): string {
  return apiConfig?.endpoint?.trim() || getEnvUrls().modelProxyLongRecordingSubmitTaskUrl;
}

function resolveModelProxyApiKey(apiConfig?: AsrApiConfig): string {
  const requestApiKey = normalizeOptionalText(apiConfig?.apiKey);
  return normalizeApiKeyHeaderValue(requestApiKey ?? requireApiKey());
}

function resolveModelProxyLongRecordingQueryTaskResultBaseUrl(
  apiConfig?: AsrApiConfig,
): string {
  const customEndpoint = apiConfig?.endpoint?.trim();
  if (customEndpoint) {
    return deriveLongRecordingQueryTaskResultBaseUrl(customEndpoint);
  }
  return getEnvUrls().modelProxyLongRecordingQueryTaskResultBaseUrl;
}

function deriveLongRecordingQueryTaskResultBaseUrl(endpoint: string): string {
  const trimmed = endpoint.replace(/\/+$/, "");
  if (trimmed.endsWith("/submit-task")) {
    return `${trimmed.slice(0, -"/submit-task".length)}/query-task-result`;
  }
  return trimmed;
}

function buildLongRecordingSubmitRequest(
  audioOssUrl: string,
  apiConfig?: AsrApiConfig,
): ModelProxyLongRecordingSubmitRequest {
  const body: ModelProxyLongRecordingSubmitRequest = {
    audioOssUrl,
  };

  if (apiConfig?.language?.trim()) {
    body.language = apiConfig.language.trim();
  }
  if (typeof apiConfig?.enableNormalization === "boolean") {
    body.enableNormalization = apiConfig.enableNormalization;
  }

  return body;
}

async function pollLongRecordingTaskResult(params: {
  apiKey: string;
  taskId: string;
  initialRequestId?: string;
  audioDurationMs?: number;
  apiConfig?: AsrApiConfig;
  logger: Logger;
}): Promise<TranscriptionResult> {
  const {
    apiKey,
    taskId,
    initialRequestId,
    audioDurationMs,
    apiConfig,
    logger,
  } = params;
  const queryBaseUrl = resolveModelProxyLongRecordingQueryTaskResultBaseUrl(apiConfig);
  let lastStatus: string | undefined;

  const pollIntervalMs = getPollIntervalMs();

  for (let attempt = 1; attempt <= DEFAULT_LONG_RECORDING_MAX_POLL_ATTEMPTS; attempt++) {
    const queryUrl = `${queryBaseUrl}/${encodeURIComponent(taskId)}`;
    let res: Response;
    try {
      res = await fetch(queryUrl, {
        method: "GET",
        headers: {
          "X-Api-Key-Id": apiKey,
        },
      });
    } catch (err: any) {
      const msg = err?.message ?? String(err);
      logger.warn(
        `[asr-query] 长录音任务查询网络异常: taskId=${taskId}, attempt=${attempt}, error=${msg} — 等待下次轮询`,
      );
      if (attempt < DEFAULT_LONG_RECORDING_MAX_POLL_ATTEMPTS) {
        await sleep(pollIntervalMs);
        continue;
      }
      return {
        ok: false,
        error: `Model Proxy ASR query network error: ${msg}`,
      };
    }

    if (!res.ok) {
      const errText = await res.text().catch(() => "");
      if (isRetryableHttpStatus(res.status)) {
        logger.warn(
          `[asr-query] 长录音任务查询暂时失败: taskId=${taskId}, attempt=${attempt}, status=${res.status}, body=${errText.slice(0, 200)} — 等待下次轮询`,
        );
        if (attempt < DEFAULT_LONG_RECORDING_MAX_POLL_ATTEMPTS) {
          await sleep(pollIntervalMs);
          continue;
        }
      } else {
        logger.error(
          `[asr-query-response] 长录音任务查询失败: taskId=${taskId}, attempt=${attempt}, status=${res.status}, body=${errText.slice(0, 500)}`,
        );
      }
      return {
        ok: false,
        error: `Model Proxy ASR query error: ${res.status} ${errText.slice(0, 200)}`,
      };
    }

    const raw = (await res.json()) as
      | ModelProxyLongRecordingStatusResponse
      | ResponseEnvelope<ModelProxyLongRecordingStatusResponse>;
    const queryEnvelopeError = buildModelProxyEnvelopeError(raw);
    if (queryEnvelopeError) {
      logger.error(
        `[asr-query-response] 长录音任务查询失败: taskId=${taskId}, attempt=${attempt}, ${queryEnvelopeError}`,
      );
      return {
        ok: false,
        error: `Model Proxy ASR 查询失败: ${queryEnvelopeError}`,
      };
    }

    const data = unwrapResponse(raw);
    const status = normalizeLongRecordingStatus(data?.status) ?? "UNKNOWN";
    const requestId = normalizeOptionalText(data?.requestId) ?? initialRequestId;

    if (status !== lastStatus) {
      logger.info(
        `[asr-query-response] 长录音任务查询响应: taskId=${taskId}, attempt=${attempt}, body=${stringifyForLog(raw) ?? "{}"}`,
      );
      logger.info(
        `[asr] Model Proxy 长录音任务状态: taskId=${taskId}, status=${status}, attempt=${attempt}, requestId=${requestId ?? "n/a"}`,
      );
      lastStatus = status;
    }

    if (status === "SUCCEEDED") {
      return buildLongRecordingSuccessResult(
        taskId,
        requestId,
        data,
        audioDurationMs,
        logger,
      );
    }

    if (LONG_RECORDING_TERMINAL_FAILURE_STATUSES.has(status)) {
      return {
        ok: false,
        error: buildLongRecordingStatusError(data, status),
      };
    }

    if (!LONG_RECORDING_RUNNING_STATUSES.has(status)) {
      return {
        ok: false,
        error: `Model Proxy ASR 返回未知任务状态: ${status}`,
      };
    }

    if (attempt < DEFAULT_LONG_RECORDING_MAX_POLL_ATTEMPTS) {
      await sleep(pollIntervalMs);
    }
  }

  return {
    ok: false,
    error:
      `Model Proxy ASR 轮询超时: taskId=${taskId}, waited=${DEFAULT_LONG_RECORDING_MAX_POLL_ATTEMPTS * pollIntervalMs}ms`,
  };
}

function buildLongRecordingSuccessResult(
  taskId: string,
  requestId: string | undefined,
  data: ModelProxyLongRecordingStatusResponse | undefined,
  audioDurationMs: number | undefined,
  logger: Logger,
): TranscriptionResult {
  const sourceTextList = normalizeLongRecordingSourceTextList(
    data?.recordResult?.sourceTextList,
  );
  const listResult = extractLongRecordingTextFromList(sourceTextList, audioDurationMs);
  const sourceText = normalizeOptionalText(data?.recordResult?.sourceText);
  const summaryText = normalizeOptionalText(data?.recordResult?.summaryResult) ?? "";
  const title = normalizeOptionalText(data?.recordResult?.title);
  const category = normalizeOptionalText(data?.recordResult?.category);
  const text = listResult.text ?? sourceText ?? summaryText;
  const status = normalizeLongRecordingStatus(data?.status) ?? "SUCCEEDED";

  if (!listResult.text && !sourceText && summaryText) {
    logger.warn(
      `[asr] Model Proxy 长录音结果缺少 sourceTextList/sourceText，已回退使用 summaryResult 作为转写文本: taskId=${taskId}`,
    );
  }

  logger.info(
    `[asr] Model Proxy 长录音转写完成: taskId=${taskId}, requestId=${requestId ?? "n/a"}, chars=${text.length}`,
  );

  return {
    ok: true,
    text,
    segments: listResult.segments,
    summary: title,
    summaryText,
    category,
    sourceInfo: {
      provider: "model-proxy",
      taskId,
      requestId,
      status,
    },
    rawResponse: data,
  };
}

function buildLongRecordingStatusError(
  data: ModelProxyLongRecordingStatusResponse | undefined,
  status: string,
): string {
  const errorMessage = normalizeOptionalText(data?.errorMessage);
  return errorMessage
    ? `Model Proxy ASR ${status}: ${errorMessage}`
    : `Model Proxy ASR ${status}`;
}

function buildModelProxyEnvelopeError(
  payload:
    | ModelProxyLongRecordingStatusResponse
    | ResponseEnvelope<ModelProxyLongRecordingStatusResponse>,
): string | undefined {
  if (!payload || typeof payload !== "object" || !("data" in payload)) {
    return undefined;
  }

  const envelope = payload as ResponseEnvelope<ModelProxyLongRecordingStatusResponse>;
  const explicitFailure = envelope.success === false;
  const message = normalizeOptionalText(envelope.message);
  if (!explicitFailure && (!message || envelope.data)) {
    return undefined;
  }

  const code = normalizeOptionalCode(envelope.code);
  if (code && message) {
    return `${code} ${message}`;
  }
  return message ?? code ?? "response envelope indicates failure";
}

function unwrapResponse(
  payload:
    | ModelProxyLongRecordingStatusResponse
    | ResponseEnvelope<ModelProxyLongRecordingStatusResponse>,
): ModelProxyLongRecordingStatusResponse | undefined {
  if (
    payload &&
    typeof payload === "object" &&
    "data" in payload &&
    payload.data &&
    typeof payload.data === "object"
  ) {
    return payload.data;
  }

  return payload as ModelProxyLongRecordingStatusResponse;
}

function normalizeApiKeyHeaderValue(apiKey: string): string {
  return apiKey.startsWith("Bearer ")
    ? apiKey.slice("Bearer ".length)
    : apiKey;
}

function normalizeLongRecordingStatus(status: unknown): string | undefined {
  return typeof status === "string" && status.trim()
    ? status.trim().toUpperCase()
    : undefined;
}

function normalizeOptionalText(value: unknown): string | undefined {
  return typeof value === "string" && value.trim()
    ? value.trim()
    : undefined;
}

function normalizeOptionalCode(value: unknown): string | undefined {
  if (typeof value === "string" && value.trim()) {
    return value.trim();
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  return undefined;
}

function normalizeOptionalInteger(value: unknown): number | undefined {
  return Number.isInteger(value)
    ? Number(value)
    : undefined;
}

function normalizeOptionalNonNegativeNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? value
    : undefined;
}

function normalizeLongRecordingSourceTextList(
  value: unknown,
): Array<{
  content: string;
  speakerId?: number;
  startTime?: number;
  endTime?: number;
}> {
  if (!Array.isArray(value)) {
    return [];
  }

  return value
    .flatMap((item) => {
      if (!item || typeof item !== "object") {
        return [];
      }

      const record = item as Record<string, unknown>;
      const content = normalizeOptionalText(record.content);
      if (!content) {
        return [];
      }

      return [{
        content,
        speakerId: normalizeOptionalInteger(record.speakerId),
        startTime: normalizeOptionalNonNegativeNumber(record.startTime),
        endTime: normalizeOptionalNonNegativeNumber(record.endTime),
      }];
    });
}

function extractLongRecordingTextFromList(
  items: Array<{
    content: string;
    speakerId?: number;
    startTime?: number;
    endTime?: number;
  }>,
  finalFallbackEndMs?: number,
): {
  text?: string;
  segments: TranscriptSegment[];
} {
  if (items.length === 0) {
    return {
      segments: [],
    };
  }

  const segments = items.map((item, index) => {
    const startMs = item.startTime ?? 0;
    const explicitEndMs = item.endTime;
    const nextStart = items[index + 1]?.startTime;
    const fallbackEndMs = index === items.length - 1
      ? normalizeOptionalNonNegativeNumber(finalFallbackEndMs)
      : undefined;
    const endMs = explicitEndMs ?? nextStart ?? fallbackEndMs ?? startMs;

    return {
      start_ms: startMs,
      end_ms: Math.max(startMs, endMs),
      text: item.content,
      speaker_id: item.speakerId,
    };
  });

  return {
    text: items.map((item) => item.content).join("\n\n"),
    segments,
  };
}

function stringifyForLog(value: unknown, maxLength = 500): string | undefined {
  if (value == null) {
    return undefined;
  }

  if (typeof value === "string") {
    const trimmed = value.trim();
    return trimmed ? trimmed.slice(0, maxLength) : undefined;
  }

  try {
    const serialized = JSON.stringify(value);
    return serialized ? serialized.slice(0, maxLength) : undefined;
  } catch {
    return String(value).slice(0, maxLength);
  }
}

async function sleep(ms: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

function isRetryableHttpStatus(status: number): boolean {
  return status === 429 || status >= 500;
}

function getHttpRetryBackoffMs(): number {
  const raw = process.env.OPENCLAW_ASR_HTTP_RETRY_BACKOFF_MS;
  if (raw) {
    const parsed = Number(raw);
    if (Number.isFinite(parsed) && parsed >= 0) {
      return parsed;
    }
  }
  return DEFAULT_HTTP_RETRY_BACKOFF_MS;
}

function getPollIntervalMs(): number {
  const raw = process.env.OPENCLAW_ASR_POLL_INTERVAL_MS;
  if (raw) {
    const parsed = Number(raw);
    if (Number.isFinite(parsed) && parsed >= 0) {
      return parsed;
    }
  }
  return DEFAULT_LONG_RECORDING_POLL_INTERVAL_MS;
}

/**
 * 带重试的 fetch 包装：
 * - HTTP 5xx / 429：按指数退避重试
 * - 网络异常（fetch throw）：按指数退避重试
 * - HTTP 4xx（除 429）/ 2xx / 3xx：直接返回，由调用方处理
 *
 * 用于 ASR submit 这类一次性请求；polling 走自己的节奏，无需此包装。
 */
async function fetchWithRetry(
  url: string,
  init: RequestInit,
  options: {
    logger: Logger;
    context: string;
    maxAttempts?: number;
    backoffMs?: number;
  },
): Promise<Response> {
  const maxAttempts = options.maxAttempts ?? DEFAULT_HTTP_MAX_ATTEMPTS;
  const baseBackoff = options.backoffMs ?? getHttpRetryBackoffMs();
  let lastError: unknown;

  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    try {
      const res = await fetch(url, init);
      if (isRetryableHttpStatus(res.status) && attempt < maxAttempts) {
        const errText = await res.text().catch(() => "");
        const delay = baseBackoff * Math.pow(2, attempt - 1);
        options.logger.warn(
          `[${options.context}] HTTP ${res.status} (attempt ${attempt}/${maxAttempts}), ${delay}ms 后重试, body=${errText.slice(0, 200)}`,
        );
        await sleep(delay);
        continue;
      }
      return res;
    } catch (err) {
      lastError = err;
      if (attempt < maxAttempts) {
        const delay = baseBackoff * Math.pow(2, attempt - 1);
        const msg = (err as any)?.message ?? String(err);
        options.logger.warn(
          `[${options.context}] 网络异常 (attempt ${attempt}/${maxAttempts}): ${msg}, ${delay}ms 后重试`,
        );
        await sleep(delay);
        continue;
      }
    }
  }

  throw lastError instanceof Error ? lastError : new Error(String(lastError));
}

function extractTranscriptSummary(text: string): string {
  const normalized = text.replace(/\s+/g, " ").trim();
  if (!normalized) {
    return "";
  }

  const sentence = normalized
    .split(/[。！？!?]/)
    .map((part) => part.trim())
    .find((part) => part.length > 0);

  const candidate = sentence || normalized;
  return candidate.length <= 120
    ? candidate
    : `${candidate.slice(0, 117).trimEnd()}...`;
}
/** 毫秒 → "MM:SS" */
function formatTimestamp(ms: number): string {
  const totalSeconds = Math.floor(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

/** 秒 → "HH:MM:SS" 或 "MM:SS" */
function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) {
    return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  }
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

function hasText(value: unknown): boolean {
  return typeof value === "string" && value.trim().length > 0;
}

function formatTranscriptSegmentText(segment: TranscriptSegment): string {
  const text = segment.text.trim();
  if (!text) {
    return text;
  }

  if (typeof segment.speaker_id === "number") {
    return `说话人${segment.speaker_id}：${text}`;
  }

  return text;
}

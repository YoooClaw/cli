/**
 * 录音同步处理器（§2 阶段 4–5）
 *
 * 统一处理 recordings.sync 请求：
 * 1. 元数据入库
 * 2. 从 OSS 下载音频 + 打点文件
 * 3. 更新状态 → synced
 * 4. 如已配置 ASR → 自动触发转写
 */

import type {
  RecordingMetadata,
  RecordingSyncResult,
  AsrConfig,
  RecordingTranscriptItem,
  RecordingTransferStatus,
} from "../types.js";
import type { Logger } from "../logger.js";
import { RecordingStorage } from "./storage.js";
import { downloadRecordingFiles } from "./downloader.js";
import { isAsrConfigured, runTranscriptionWorkflow } from "./asr.js";
import { canStartTranscription } from "./state-machine.js";

export interface RecordingStatusEvent {
  recordingId: string;
  transfer_status: RecordingTransferStatus;
  audioFile?: string;
  srtFile?: string;
  transcriptDataFile?: string;
  transcriptFile?: string;
  transcript?: RecordingTranscriptItem[];
  summary?: string;
  title: string;
  updatedAt: string;
  error?: string;
}

export interface RecordingSyncOptions {
  notifyStatus?: (event: RecordingStatusEvent) => void;
}

function emitRecordingStatus(
  recordingId: string,
  storage: RecordingStorage,
  logger: Logger,
  notifyStatus?: RecordingSyncOptions["notifyStatus"],
  error?: string,
  extras?: Pick<RecordingStatusEvent, "transcript" | "summary" | "title">,
): void {
  if (!notifyStatus) {
    return;
  }

  const entry = storage.findById(recordingId);
  if (!entry) {
    return;
  }

  const title = extras?.title?.trim()
    || entry.title?.trim()
    || entry.metadata.name?.trim()
    || entry.id;
  const persistedError = entry.lastError?.trim() || undefined;

  try {
    notifyStatus({
      recordingId: entry.id,
      transfer_status: entry.status,
      audioFile: entry.audioFile,
      srtFile: entry.srtFile,
      transcriptDataFile: entry.transcriptDataFile,
      transcriptFile: entry.transcriptFile,
      transcript: extras?.transcript,
      summary: extras?.summary,
      title,
      updatedAt: entry.updatedAt,
      error: error ?? persistedError,
    });
  } catch (err: any) {
    logger.error(
      `[recording-status] 状态事件发送失败: ${recordingId}, ${err?.message ?? err}`,
    );
  }
}

async function runRecordingSyncInBackground(
  metadata: RecordingMetadata,
  recordingId: string,
  storage: RecordingStorage,
  asrConfig: AsrConfig | undefined,
  logger: Logger,
  options: RecordingSyncOptions,
): Promise<void> {
  const audioDestPath = storage.getAudioFilePath(
    recordingId,
    metadata.oss_audio_url,
  );
  const srtDestPath = storage.getSrtFilePath(recordingId);

  logger.info(
    `[recording-sync] 开始下载录音文件: ${recordingId}, audio=${metadata.oss_audio_url}`,
  );

  const downloadResult = await downloadRecordingFiles(
    metadata.oss_audio_url,
    metadata.oss_srt_url,
    audioDestPath,
    srtDestPath,
    logger,
  );

  if (!downloadResult.audio.ok) {
    const error = `音频下载失败: ${downloadResult.audio.error}`;
    logger.error(`[recording-sync] ${error}: ${recordingId}`);
    storage.updateStatus(recordingId, "sync_failed");
    storage.setLastError(recordingId, error);
    emitRecordingStatus(
      recordingId,
      storage,
      logger,
      options.notifyStatus,
      error,
    );
    return;
  }

  storage.setLastError(recordingId, undefined);
  storage.setAudioFile(
    recordingId,
    storage.buildAudioFilename(recordingId, metadata.oss_audio_url),
  );
  if (downloadResult.srt?.ok) {
    storage.setSrtFile(recordingId, storage.buildSrtFilename(recordingId));
  }

  storage.updateStatus(recordingId, "synced");
  logger.info(`[recording-sync] 录音已同步: ${recordingId}`);
  emitRecordingStatus(recordingId, storage, logger, options.notifyStatus);

  if (isAsrConfigured(asrConfig) && canStartTranscription("synced")) {
    await triggerTranscription(
      recordingId,
      storage,
      asrConfig!,
      logger,
      options,
    );
  }
}

/**
 * 处理 recordings.sync 请求的核心逻辑
 */
export async function handleRecordingSync(
  recordingId: string,
  metadata: RecordingMetadata,
  storage: RecordingStorage,
  asrConfig: AsrConfig | undefined,
  logger: Logger,
  options: RecordingSyncOptions = {},
): Promise<RecordingSyncResult> {
  const existing = storage.findById(recordingId);
  const shouldDownloadAndSync =
    !existing
    || existing.metadata.oss_audio_url !== metadata.oss_audio_url
    || !existing.audioFile
    || existing.status === "syncing_openclaw"
    || existing.status === "sync_failed";

  storage.ingest(recordingId, metadata);
  if (shouldDownloadAndSync) {
    runRecordingSyncInBackground(
      metadata,
      recordingId,
      storage,
      asrConfig,
      logger,
      options,
    ).catch((err) => {
      const error = `录音同步失败: ${err?.message ?? err}`;
      logger.error(`[recording-sync] ${error}: ${recordingId}`);
      const current = storage.findById(recordingId);
      if (current?.status === "syncing_openclaw") {
        storage.updateStatus(recordingId, "sync_failed");
      }
      storage.setLastError(recordingId, error);
      emitRecordingStatus(
        recordingId,
        storage,
        logger,
        options.notifyStatus,
        error,
      );
    });
  } else {
    logger.info(
      `[recording-sync] 录音已存在且音频未变化，跳过重复下载: ${recordingId}`,
    );
    emitRecordingStatus(recordingId, storage, logger, options.notifyStatus);

    const current = storage.findById(recordingId);
    if (current?.status === "synced" && isAsrConfigured(asrConfig)) {
      triggerTranscription(
        recordingId,
        storage,
        asrConfig!,
        logger,
        options,
      ).catch((err) => {
        logger.error(
          `[asr-trigger] 转写触发失败: ${recordingId}, ${err?.message ?? err}`,
        );
      });
    }
  }

  const current = storage.findById(recordingId);
  return {
    ok: true,
    recordingId,
    transfer_status: current?.status ?? "syncing_openclaw",
    ...(current?.lastError ? { error: current.lastError } : {}),
  };
}

/**
 * 触发 ASR 转写（可从 sync handler 自动触发或手动重试调用）
 */
export async function triggerTranscription(
  recordingId: string,
  storage: RecordingStorage,
  asrConfig: AsrConfig,
  logger: Logger,
  options: RecordingSyncOptions = {},
): Promise<void> {
  const entry = storage.findById(recordingId);
  if (!entry) {
    logger.warn(`[asr-trigger] 录音不存在: ${recordingId}`);
    return;
  }

  if (!canStartTranscription(entry.status)) {
    logger.warn(
      `[asr-trigger] 当前状态不允许转写: ${recordingId} (status=${entry.status})`,
    );
    return;
  }

  // 状态 → transcribing
  storage.updateStatus(recordingId, "transcribing");
  storage.setLastError(recordingId, undefined);
  emitRecordingStatus(recordingId, storage, logger, options.notifyStatus);

  const audioFilePath = storage.getAudioFilePath(recordingId);

  let result: Awaited<ReturnType<typeof runTranscriptionWorkflow>>;
  try {
    result = await runTranscriptionWorkflow({
      audioFilePath,
      audioOssUrl: entry.metadata.oss_audio_url,
      config: asrConfig,
      markers: entry.metadata.markers ?? [],
      recordingName: entry.metadata.name,
      durationSec: entry.metadata.duration_sec,
      createdAt: entry.metadata.created_at,
      transcriptDataDir: storage.getTranscriptDataDir(),
      transcriptsDir: storage.getTranscriptsDir(),
      summariesDir: storage.getSummariesDir(),
      recordingId,
      logger,
    });
  } catch (err: any) {
    const error = `转写任务异常: ${err?.message ?? err}`;
    logger.error(`[asr-trigger] ${error}: ${recordingId}`);
    result = { ok: false, error };
  }

  if (result.ok && result.transcriptFilename) {
    if (result.transcriptDataFilename) {
      storage.setTranscriptDataFile(recordingId, result.transcriptDataFilename);
    }
    storage.setTranscriptFile(recordingId, result.transcriptFilename);
    if (result.summaryFilename) {
      storage.setSummaryFile(recordingId, result.summaryFilename);
    }
    storage.setTitle(recordingId, result.title);
    storage.updateStatus(recordingId, "transcribed");
    emitRecordingStatus(
      recordingId,
      storage,
      logger,
      options.notifyStatus,
      undefined,
      {
        transcript: result.transcript ?? [],
        summary: result.summary,
        title: result.title ?? "",
      },
    );
    logger.info(
      `[asr-trigger] 转写完成: ${recordingId}, summary="${result.summary}"`,
    );
  } else {
    const error = result.error ?? "ASR 转写失败";
    storage.updateStatus(recordingId, "transcribe_failed");
    storage.setLastError(recordingId, error);
    emitRecordingStatus(
      recordingId,
      storage,
      logger,
      options.notifyStatus,
      error,
    );
    logger.error(
      `[asr-trigger] 转写失败: ${recordingId}, error=${error}`,
    );
  }
}

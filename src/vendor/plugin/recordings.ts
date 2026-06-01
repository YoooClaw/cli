import type { IncomingMessage, ServerResponse } from "node:http";
import type { OpenClawPluginApi } from "openclaw/plugin-sdk";
import type { Logger } from "../logger.js";
import {
  RecordingStorage,
  canStartTranscription,
  extractSourceTextListFromDocument,
  extractTranscriptTitleFromDocument,
  handleRecordingSync,
  initializeAsr,
  triggerTranscription,
  type RecordingIndexEntry,
  type RecordingStatusEvent,
  validateAsrConfig,
} from "../recording/index.js";
import { RELAY_INTERNAL_HTTP_HEADER } from "../tunnel/http-proxy.js";
import type { RelayTunnelService } from "../tunnel/service.js";
import type {
  HttpRecordingBody,
  RecordingAsrInitParams,
  RecordingDetail,
  RecordingListItem,
  RecordingListParams,
  RecordingRenameParams,
  RecordingRetranscribeParams,
  RecordingStatusParams,
  RecordingSyncParams,
  RecordingTransferStatus,
} from "../types.js";
import {
  readBody,
  trimToUndefined,
  type GatewayMethodRegistrar,
} from "./shared.js";

const RECORDING_TRANSFER_STATUSES = new Set<RecordingTransferStatus>([
  "receiving_failed",
  "receiving",
  "pending_oss_upload",
  "uploading_oss",
  "oss_uploaded",
  "syncing_openclaw",
  "sync_failed",
  "synced",
  "transcribing",
  "transcribe_failed",
  "transcribed",
]);

function isRecordingTransferStatus(
  value: string,
): value is RecordingTransferStatus {
  return RECORDING_TRANSFER_STATUSES.has(value as RecordingTransferStatus);
}

function buildRecordingListItem(entry: RecordingIndexEntry): RecordingListItem {
  return {
    recordingId: entry.id,
    name: entry.metadata.name,
    duration_sec: entry.metadata.duration_sec,
    file_size_bytes: entry.metadata.file_size_bytes,
    created_at: entry.metadata.created_at,
    transfer_status: entry.status,
    marker_count: entry.metadata.markers?.length ?? 0,
    has_audio: !!entry.audioFile,
    has_srt: !!entry.srtFile,
    has_transcript: !!entry.transcriptFile,
    audioFile: entry.audioFile,
    transcriptDataFile: entry.transcriptDataFile,
    transcriptFile: entry.transcriptFile,
    updatedAt: entry.updatedAt,
    ...(entry.lastError ? { error: entry.lastError } : {}),
  };
}

function isRelayInternalHttpRequest(req: IncomingMessage): boolean {
  const header = req.headers?.[RELAY_INTERNAL_HTTP_HEADER];
  if (Array.isArray(header)) {
    return header.includes("1");
  }
  return header === "1";
}

function buildRecordingDetail(
  entry: RecordingIndexEntry,
  extras?: Partial<Pick<
    RecordingDetail,
    "summary" | "title" | "transcript" | "transcriptData"
  >>,
): RecordingDetail {
  const title = entry.title?.trim()
    || extras?.title?.trim()
    || entry.metadata.name?.trim()
    || entry.id;

  return {
    recordingId: entry.id,
    name: entry.metadata.name,
    duration_sec: entry.metadata.duration_sec,
    file_size_bytes: entry.metadata.file_size_bytes,
    created_at: entry.metadata.created_at,
    location: entry.metadata.location,
    oss_audio_url: entry.metadata.oss_audio_url,
    oss_srt_url: entry.metadata.oss_srt_url,
    markers: entry.metadata.markers ?? [],
    transfer_status: entry.status,
    audioFile: entry.audioFile,
    srtFile: entry.srtFile,
    transcriptDataFile: entry.transcriptDataFile,
    transcriptFile: entry.transcriptFile,
    summaryFile: entry.summaryFile,
    title,
    summary: extras?.summary,
    transcript: extras?.transcript,
    transcriptData: extras?.transcriptData,
    ingestedAt: entry.ingestedAt,
    updatedAt: entry.updatedAt,
    ...(entry.lastError ? { error: entry.lastError } : {}),
  };
}

interface RecordingInterfaceDeps {
  api: OpenClawPluginApi;
  logger: Logger;
  asrDataDir: string;
  getRecordingStorage: () => RecordingStorage | null;
  notifyRecordingStatus: (event: RecordingStatusEvent) => void;
  registerGatewayMethod: GatewayMethodRegistrar;
  shouldBroadcastStatusOnHttp: () => boolean;
  tunnelService?: RelayTunnelService | null;
}

export function registerRecordingInterfaces(
  deps: RecordingInterfaceDeps,
): void {
  const {
    api,
    logger,
    asrDataDir,
    getRecordingStorage,
    notifyRecordingStatus,
    registerGatewayMethod,
    shouldBroadcastStatusOnHttp,
    tunnelService,
  } = deps;

  registerGatewayMethod("recordings.sync", async ({ params, respond }) => {
    const recordingStorage = getRecordingStorage();
    if (!recordingStorage) {
      respond(false, null, {
        code: "NOT_READY",
        message: "Recording storage service not ready",
      });
      return;
    }

    const {
      recordingId: rawRecordingId,
      recording,
      asr,
    } = params as unknown as RecordingSyncParams;
    const recordingId = trimToUndefined(rawRecordingId);
    if (!recordingId) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: "recordingId is required",
      });
      return;
    }

    if (!recording || !recording.oss_audio_url || !recording.created_at) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: "recording with oss_audio_url and created_at is required",
      });
      return;
    }

    const asrError = asr ? validateAsrConfig(asr) : undefined;
    if (asrError) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: asrError,
      });
      return;
    }

    const result = await handleRecordingSync(
      recordingId,
      recording,
      recordingStorage,
      asr,
      logger,
      { notifyStatus: notifyRecordingStatus },
    );
    respond(
      result.ok,
      result,
      result.ok
        ? undefined
        : {
            code: "SYNC_FAILED",
            message: result.error ?? "Unknown error",
          },
    );
  });

  registerGatewayMethod("recordings.list", async ({ params, respond }) => {
    const recordingStorage = getRecordingStorage();
    if (!recordingStorage) {
      respond(false, null, {
        code: "NOT_READY",
        message: "Recording storage service not ready",
      });
      return;
    }

    const rawStatus = trimToUndefined(
      (params as RecordingListParams | undefined)?.status,
    );
    if (rawStatus && !isRecordingTransferStatus(rawStatus)) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: `invalid recording status: ${rawStatus}`,
      });
      return;
    }

    const status = rawStatus as RecordingTransferStatus | undefined;
    const entries = status
      ? recordingStorage.listByStatus(status)
      : recordingStorage.listAll();

    respond(true, {
      total: entries.length,
      recordings: entries.map(buildRecordingListItem),
    });
  });

  registerGatewayMethod("recordings.status", async ({ params, respond }) => {
    const recordingStorage = getRecordingStorage();
    if (!recordingStorage) {
      respond(false, null, {
        code: "NOT_READY",
        message: "Recording storage service not ready",
      });
      return;
    }

    const { recordingId } = params as unknown as RecordingStatusParams;
    if (!recordingId) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: "recordingId is required",
      });
      return;
    }

    const entry = recordingStorage.findById(recordingId);
    if (!entry) {
      respond(false, null, {
        code: "NOT_FOUND",
        message: `Recording not found: ${recordingId}`,
      });
      return;
    }

    respond(
      true,
      (() => {
        const transcriptDocument =
          recordingStorage.readTranscriptDocument(recordingId);

        return buildRecordingDetail(entry, {
          summary: recordingStorage.readSummary(recordingId),
          title: extractTranscriptTitleFromDocument(transcriptDocument),
          transcript: extractSourceTextListFromDocument(transcriptDocument),
          transcriptData: transcriptDocument,
        });
      })(),
    );
  });

  registerGatewayMethod("recordings.rename", async ({ params, respond }) => {
    const recordingStorage = getRecordingStorage();
    if (!recordingStorage) {
      respond(false, null, {
        code: "NOT_READY",
        message: "Recording storage service not ready",
      });
      return;
    }

    const { recordingId: rawRecordingId, name: rawName } =
      params as unknown as RecordingRenameParams;
    const recordingId = trimToUndefined(rawRecordingId);
    const name = trimToUndefined(rawName);
    if (!recordingId) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: "recordingId is required",
      });
      return;
    }
    if (!name) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: "name is required",
      });
      return;
    }

    const entry = recordingStorage.findById(recordingId);
    if (!entry) {
      respond(false, null, {
        code: "NOT_FOUND",
        message: `Recording not found: ${recordingId}`,
      });
      return;
    }

    const updated = recordingStorage.rename(recordingId, name);
    respond(true, buildRecordingDetail(updated));
  });

  registerGatewayMethod("recordings.delete", async ({ params, respond }) => {
    const recordingStorage = getRecordingStorage();
    if (!recordingStorage) {
      respond(false, null, {
        code: "NOT_READY",
        message: "Recording storage service not ready",
      });
      return;
    }

    const { recordingId, deleteRemote } = params as {
      recordingId: string;
      deleteRemote?: boolean;
    };
    if (!recordingId) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: "recordingId is required",
      });
      return;
    }

    if (deleteRemote) {
      const deleted = recordingStorage.delete(recordingId);
      respond(
        deleted,
        { ok: deleted, recordingId, deletedRemote: true },
        deleted
          ? undefined
          : {
              code: "NOT_FOUND",
              message: `Recording not found: ${recordingId}`,
            },
      );
      return;
    }

    const deleted = recordingStorage.delete(recordingId, {
      localOnly: true,
    });
    respond(
      deleted,
      { ok: deleted, recordingId, deletedRemote: false },
      deleted
        ? undefined
        : {
            code: "NOT_FOUND",
            message: `Recording not found: ${recordingId}`,
          },
    );
  });

  registerGatewayMethod(
    "recordings.retranscribe",
    async ({ params, respond }) => {
      const recordingStorage = getRecordingStorage();
      if (!recordingStorage) {
        respond(false, null, {
          code: "NOT_READY",
          message: "Recording storage service not ready",
        });
        return;
      }

      const { recordingId, asr } =
        params as unknown as RecordingRetranscribeParams;
      if (!recordingId) {
        respond(false, null, {
          code: "INVALID_PARAMS",
          message: "recordingId is required",
        });
        return;
      }

      const asrError = validateAsrConfig(asr);
      if (asrError) {
        respond(false, null, {
          code: "ASR_NOT_CONFIGURED",
          message: asrError,
        });
        return;
      }

      const entry = recordingStorage.findById(recordingId);
      if (!entry) {
        respond(false, null, {
          code: "NOT_FOUND",
          message: `Recording not found: ${recordingId}`,
        });
        return;
      }

      if (!canStartTranscription(entry.status)) {
        respond(false, null, {
          code: "INVALID_STATE",
          message: `Recording status does not allow retranscribe: ${entry.status}`,
        });
        return;
      }

      triggerTranscription(recordingId, recordingStorage, asr, logger, {
        notifyStatus: notifyRecordingStatus,
      }).catch((err) => {
        logger.error(
          `[recordings.retranscribe] 重试转写失败: ${recordingId}, ${err?.message ?? err}`,
        );
      });

      respond(true, { ok: true, recordingId, message: "转写已重新触发" });
    },
  );

  registerGatewayMethod("recordings.asr.init", async ({ params, respond }) => {
    const { asr } = params as unknown as RecordingAsrInitParams;
    const asrError = validateAsrConfig(asr);
    if (asrError) {
      respond(false, null, {
        code: "INVALID_PARAMS",
        message: asrError,
      });
      return;
    }

    const result = await initializeAsr(asr, asrDataDir, logger);
    respond(
      result.ok,
      result,
      result.ok
        ? undefined
        : {
            code: "ASR_INIT_FAILED",
            message: result.error ?? "ASR init failed",
          },
    );
  });

  api.registerHttpRoute({
    path: "/recordings",
    auth: "gateway",
    async handler(req: IncomingMessage, res: ServerResponse) {
      if (req.method !== "POST") {
        res.writeHead(405, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Method Not Allowed" }));
        return;
      }

      if (!isRelayInternalHttpRequest(req)) {
        await tunnelService?.deactivateForExternalTunnel("POST /recordings");
      }

      const recordingStorage = getRecordingStorage();
      if (!recordingStorage) {
        res.writeHead(503, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Service Not Ready" }));
        return;
      }

      let body: HttpRecordingBody;
      try {
        const raw = await readBody(req);
        body = JSON.parse(raw);
      } catch {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Invalid JSON" }));
        return;
      }

      const recordingId = trimToUndefined(body.recordingId);
      if (!recordingId) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            ok: false,
            error: "recordingId is required",
          }),
        );
        return;
      }

      if (!body.recording || !body.recording.oss_audio_url) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            ok: false,
            error: "recording with oss_audio_url is required",
          }),
        );
        return;
      }

      const asrError = body.asr ? validateAsrConfig(body.asr) : undefined;
      if (asrError) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: asrError }));
        return;
      }

      const result = await handleRecordingSync(
        recordingId,
        body.recording,
        recordingStorage,
        body.asr,
        logger,
        shouldBroadcastStatusOnHttp()
          ? { notifyStatus: notifyRecordingStatus }
          : undefined,
      );

      res.writeHead(result.ok ? 200 : 500, {
        "Content-Type": "application/json",
      });
      res.end(JSON.stringify(result));
    },
  } as any);

  logger.info(
    "Gateway 录音方法已注册: recordings.sync / recordings.list / recordings.status / recordings.rename / recordings.delete / recordings.retranscribe / recordings.asr.init",
  );
  logger.info("HTTP 录音端点已注册: POST /recordings");
}

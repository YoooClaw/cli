/**
 * 录音本地存储管理（§5.2）
 *
 * 目录结构：
 *   <storageDir>/recordings/
 *   ├── audio/            # 原始音频 + 打点文件
 *   │   ├── 2026-03-23_14-32.ogg
 *   │   └── 2026-03-23_14-32.srt
 *   ├── transcript-data/  # 转写 JSON（主存储）
 *   │   └── 2026-03-23_14-32.json
 *   ├── transcripts/      # 转写文本
 *   │   └── 2026-03-23_14-32_与王总沟通产品方向.md
 *   ├── summaries/        # 转写摘要
 *   │   └── 2026-03-23_14-32.md
 *   └── index.json        # 元数据索引
 */

import {
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  rmSync,
  readdirSync,
  statSync,
} from "node:fs";
import { join, basename } from "node:path";
import type {
  RecordingMetadata,
  RecordingTransferStatus,
} from "../types.js";
import { transition } from "./state-machine.js";
import type { Logger } from "../logger.js";
import { currentClientLabel } from "../ingest-context.js";
import {
  buildTranscriptDataFilename as buildTranscriptDataJsonFilename,
  buildTranscriptDocument,
  extractTranscriptSummaryFromDocument,
  extractTranscriptTextFromDocument,
  extractTranscriptTitleFromDocument,
  parseTranscriptDocument,
} from "./transcript-document.js";

/**
 * 从 OSS URL 中提取音频文件扩展名（含点号，如 ".ogg"）
 * 默认返回 ".ogg"（小硬件录音为 Opus/OGG 格式）
 */
function extractAudioExt(ossUrl: string): string {
  try {
    const pathname = new URL(ossUrl).pathname;
    const m = pathname.match(/(\.[a-z0-9]+)(?:\?|$)/i);
    if (m) return m[1].toLowerCase();
  } catch {
    const m = ossUrl.match(/(\.[a-z0-9]+)(?:\?|$)/i);
    if (m) return m[1].toLowerCase();
  }
  return ".ogg"; // 硬件默认录音格式（Opus/OGG）
}

// ─── Types ───

/** 持久化到 index.json 的单条录音记录 */
export interface RecordingIndexEntry {
  /** 录音 ID，基于文件名（如 "2026-03-23_14-32"） */
  id: string;
  /** 原始元数据 */
  metadata: RecordingMetadata;
  /** 来源客户端 label；旧数据无该字段，查询时视为 legacy */
  clientLabel?: string;
  /** 当前插件侧状态 */
  status: RecordingTransferStatus;
  /** 本地音频文件相对路径 */
  audioFile?: string;
  /** 本地打点文件相对路径 */
  srtFile?: string;
  /** 本地转写文件相对路径 */
  transcriptFile?: string;
  /** 本地转写 JSON 文件相对路径 */
  transcriptDataFile?: string;
  /** 本地摘要文件相对路径 */
  summaryFile?: string;
  /** 转写标题 */
  title?: string;
  /** 最近一次失败原因（同步失败 / 转写失败） */
  lastError?: string;
  /** 入库时间 ISO 8601 */
  ingestedAt: string;
  /** 最后更新时间 ISO 8601 */
  updatedAt: string;
}

/** 录音存储索引（整个 index.json） */
interface RecordingIndex {
  recordings: RecordingIndexEntry[];
}

export interface RecordingStorageContext {
  stateDir: string;
  workspaceDir?: string;
}

// ─── Constants ───

const RECORDINGS_DIR = "recordings";
const AUDIO_DIR = "audio";
const TRANSCRIPT_DATA_DIR = "transcript-data";
const TRANSCRIPTS_DIR = "transcripts";
const SUMMARIES_DIR = "summaries";
const INDEX_FILE = "index.json";

function stripMarkdownFence(markdown: string): string {
  return markdown.replace(/\r\n/g, "\n");
}

function deriveTitleFromTranscriptPath(
  transcriptFile: string | undefined,
  recordingId: string,
): string | undefined {
  if (!transcriptFile) return undefined;
  const name = basename(transcriptFile, ".md");
  const prefix = `${recordingId}_`;
  if (name.startsWith(prefix)) {
    const derived = name.slice(prefix.length).trim();
    return derived || undefined;
  }
  return undefined;
}

function extractTranscriptContent(markdown: string): string {
  const normalized = stripMarkdownFence(markdown);
  const firstDivider = normalized.indexOf("\n---\n");
  let body = firstDivider >= 0
    ? normalized.slice(firstDivider + "\n---\n".length)
    : normalized;

  if (body.startsWith("\n")) {
    body = body.slice(1);
  }

  if (body.startsWith("### 关键点")) {
    const secondDivider = body.indexOf("\n---\n");
    if (secondDivider >= 0) {
      body = body.slice(secondDivider + "\n---\n".length);
      if (body.startsWith("\n")) {
        body = body.slice(1);
      }
    }
  }

  const lines = body
    .split("\n")
    .filter((line) => !/^\*\*\[关键点 .+\]\*\*$/.test(line.trim()))
    .filter((line) => !/^- \*\*\[关键点 .+\]\*\*$/.test(line.trim()));

  return lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}

// ─── Public Functions ───

/**
 * 解析录音存储根目录（复用通知同级的 stateDir 路径）
 */
export function resolveRecordingStorageDir(
  ctx: RecordingStorageContext,
  logger: Pick<Logger, "info" | "warn">,
): string {
  const stateRecDir = join(
    ctx.stateDir,
    "plugins",
    "phone-notifications",
    RECORDINGS_DIR,
  );

  try {
    mkdirSync(stateRecDir, { recursive: true });
    logger.info(`录音将写入 stateDir 路径: ${stateRecDir}`);
    return stateRecDir;
  } catch {
    // fallback
  }

  if (ctx.workspaceDir) {
    const wsRecDir = join(ctx.workspaceDir, RECORDINGS_DIR);
    try {
      mkdirSync(wsRecDir, { recursive: true });
      logger.warn(`stateDir 不可用，录音已回退到 workspace 路径: ${wsRecDir}`);
      return wsRecDir;
    } catch {
      // fallthrough
    }
  }

  throw new Error(`录音存储目录不可用: ${stateRecDir}`);
}

// ─── RecordingStorage Class ───

export class RecordingStorage {
  private readonly dir: string;
  private readonly audioDir: string;
  private readonly transcriptDataDir: string;
  private readonly transcriptsDir: string;
  private readonly summariesDir: string;
  private readonly indexPath: string;
  private index: RecordingIndex = { recordings: [] };

  constructor(
    dir: string,
    private readonly logger: Logger,
  ) {
    this.dir = dir;
    this.audioDir = join(dir, AUDIO_DIR);
    this.transcriptDataDir = join(dir, TRANSCRIPT_DATA_DIR);
    this.transcriptsDir = join(dir, TRANSCRIPTS_DIR);
    this.summariesDir = join(dir, SUMMARIES_DIR);
    this.indexPath = join(dir, INDEX_FILE);
  }

  /** 初始化目录结构并加载索引 */
  async init(): Promise<void> {
    mkdirSync(this.audioDir, { recursive: true });
    mkdirSync(this.transcriptDataDir, { recursive: true });
    mkdirSync(this.transcriptsDir, { recursive: true });
    mkdirSync(this.summariesDir, { recursive: true });
    this.loadIndex();
    this.logger.info(
      `录音存储已初始化: ${this.dir}（共 ${this.index.recordings.length} 条记录）`,
    );
  }

  /** 获取音频目录路径 */
  getAudioDir(): string {
    return this.audioDir;
  }

  /** 获取转写 JSON 目录路径 */
  getTranscriptDataDir(): string {
    return this.transcriptDataDir;
  }

  /** 获取转写目录路径 */
  getTranscriptsDir(): string {
    return this.transcriptsDir;
  }

  /** 获取摘要目录路径 */
  getSummariesDir(): string {
    return this.summariesDir;
  }

  // ─── 录音入库 ───

  /**
   * 收到 recordings.sync 后，将元数据写入索引。
   * 如果同 ID 录音已存在则更新，否则新建。
   * 返回录音 ID。
   */
  ingest(recordingId: string, metadata: RecordingMetadata): string {
    const id = recordingId;
    const clientLabel = currentClientLabel("default");
    const existing = this.findById(id);

    if (existing) {
      const sameAudioUrl = existing.metadata.oss_audio_url === metadata.oss_audio_url;
      const canPreserveSyncState =
        sameAudioUrl
        && !!existing.audioFile
        && existing.status !== "syncing_openclaw"
        && existing.status !== "sync_failed";

      if (canPreserveSyncState) {
        existing.metadata = metadata;
        existing.clientLabel = clientLabel;
        existing.updatedAt = new Date().toISOString();
        this.saveIndex();
        this.logger.info(
          `录音元数据已更新: ${id}（同音频，保留状态 ${existing.status}）`,
        );
        return id;
      }

      // 更新已有记录（覆盖上传/本地文件缺失场景）
      if (existing.transcriptDataFile) {
        rmSync(join(this.dir, existing.transcriptDataFile), { force: true });
      }
      if (existing.transcriptFile) {
        rmSync(join(this.dir, existing.transcriptFile), { force: true });
      }
      if (existing.summaryFile) {
        rmSync(join(this.dir, existing.summaryFile), { force: true });
      }
      existing.metadata = metadata;
      existing.clientLabel = clientLabel;
      existing.status = "syncing_openclaw";
      existing.transcriptDataFile = undefined;
      existing.transcriptFile = undefined;
      existing.summaryFile = undefined;
      existing.title = undefined;
      existing.lastError = undefined;
      existing.updatedAt = new Date().toISOString();
      this.logger.info(`录音元数据已更新: ${id}`);
    } else {
      const entry: RecordingIndexEntry = {
        id,
        metadata,
        clientLabel,
        status: "syncing_openclaw",
        ingestedAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      };
      this.index.recordings.push(entry);
      this.logger.info(`录音元数据已入库: ${id}`);
    }

    this.saveIndex();
    return id;
  }

  // ─── 状态管理 ───

  /**
   * 更新录音传输状态（严格状态机校验）
   */
  updateStatus(
    recordingId: string,
    newStatus: RecordingTransferStatus,
  ): RecordingIndexEntry {
    const entry = this.findById(recordingId);
    if (!entry) {
      throw new Error(`录音不存在: ${recordingId}`);
    }

    const validatedStatus = transition(entry.status, newStatus);
    entry.status = validatedStatus;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
    this.logger.info(`录音状态更新: ${recordingId} → ${newStatus}`);
    return entry;
  }

  /**
   * 记录本地音频文件已下载
   */
  setAudioFile(recordingId: string, filename: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    entry.audioFile = `${AUDIO_DIR}/${filename}`;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 记录本地打点文件已下载
   */
  setSrtFile(recordingId: string, filename: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    entry.srtFile = `${AUDIO_DIR}/${filename}`;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 记录转写 JSON 文件路径
   */
  setTranscriptDataFile(recordingId: string, filename: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    const nextTranscriptDataFile = `${TRANSCRIPT_DATA_DIR}/${filename}`;
    if (entry.transcriptDataFile && entry.transcriptDataFile !== nextTranscriptDataFile) {
      rmSync(join(this.dir, entry.transcriptDataFile), { force: true });
    }
    entry.transcriptDataFile = nextTranscriptDataFile;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 记录转写文件路径
   */
  setTranscriptFile(recordingId: string, filename: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    const nextTranscriptFile = `${TRANSCRIPTS_DIR}/${filename}`;
    if (entry.transcriptFile && entry.transcriptFile !== nextTranscriptFile) {
      rmSync(join(this.dir, entry.transcriptFile), { force: true });
    }
    entry.transcriptFile = nextTranscriptFile;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 记录摘要文件路径
   */
  setSummaryFile(recordingId: string, filename: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    const nextSummaryFile = `${SUMMARIES_DIR}/${filename}`;
    if (entry.summaryFile && entry.summaryFile !== nextSummaryFile) {
      rmSync(join(this.dir, entry.summaryFile), { force: true });
    }
    entry.summaryFile = nextSummaryFile;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 记录转写标题
   */
  setTitle(recordingId: string, title?: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    entry.title = title?.trim() || undefined;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 记录最近一次失败原因；传 undefined 表示清除错误
   */
  setLastError(recordingId: string, error?: string): void {
    const entry = this.findById(recordingId);
    if (!entry) return;
    entry.lastError = error?.trim() || undefined;
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
  }

  /**
   * 读取摘要文本
   */
  readSummary(recordingId: string): string | undefined {
    const entry = this.findById(recordingId);
    if (!entry) return undefined;

    if (entry.summaryFile) {
      const summary = this.readRelativeTextFile(entry.summaryFile);
      if (summary?.trim()) {
        return summary.trim();
      }
    }

    const transcriptDoc = entry.transcriptDataFile
      ? this.readRelativeTranscriptDocument(entry.transcriptDataFile)
      : undefined;
    return extractTranscriptSummaryFromDocument(transcriptDoc);
  }

  /**
   * 优先从 transcript JSON 中读取正文，旧数据回退读取 Markdown。
   */
  readTranscript(recordingId: string): string | undefined {
    const entry = this.findById(recordingId);
    if (!entry) return undefined;

    if (entry.transcriptDataFile) {
      const transcriptDoc = this.readRelativeTranscriptDocument(entry.transcriptDataFile);
      const transcriptFromJson = extractTranscriptTextFromDocument(transcriptDoc);
      if (transcriptFromJson !== undefined) {
        return transcriptFromJson;
      }
    }

    if (!entry.transcriptFile) return undefined;
    const markdown = this.readRelativeTextFile(entry.transcriptFile);
    if (!markdown) return undefined;
    const transcript = extractTranscriptContent(markdown);
    return transcript || undefined;
  }

  /**
   * 读取结构化 transcript JSON。
   */
  readTranscriptDocument(recordingId: string) {
    const entry = this.findById(recordingId);
    if (!entry?.transcriptDataFile) {
      return undefined;
    }
    return this.readRelativeTranscriptDocument(entry.transcriptDataFile);
  }

  // ─── 查询 ───

  findById(id: string): RecordingIndexEntry | undefined {
    return this.index.recordings.find((r) => r.id === id);
  }

  listAll(): RecordingIndexEntry[] {
    return [...this.index.recordings].sort(
      (a, b) => b.metadata.created_at.localeCompare(a.metadata.created_at),
    );
  }

  listByStatus(status: RecordingTransferStatus): RecordingIndexEntry[] {
    return this.listAll().filter((r) => r.status === status);
  }

  rename(recordingId: string, name: string): RecordingIndexEntry {
    const entry = this.findById(recordingId);
    if (!entry) {
      throw new Error(`录音不存在: ${recordingId}`);
    }

    entry.metadata = {
      ...entry.metadata,
      name,
    };
    entry.updatedAt = new Date().toISOString();
    this.saveIndex();
    this.logger.info(`录音名称已更新: ${recordingId} → ${name}`);
    return entry;
  }

  // ─── 删除 ───

  /**
   * 删除录音及其本地文件（约束 R-4 安全删除）
   *
   * @param opts.localOnly  仅删除本地文件（音频/打点/转写），保留索引条目。
   *                        App 端"仅删除本地"时使用此模式，插件侧保留元数据记录。
   *                        默认 false — 完整删除（文件 + 索引）。
   */
  delete(
    recordingId: string,
    opts?: { localOnly?: boolean },
  ): boolean {
    const entry = this.findById(recordingId);
    if (!entry) return false;

    // 删除本地文件
    if (entry.audioFile) {
      const audioPath = join(this.dir, entry.audioFile);
      rmSync(audioPath, { force: true });
    }
    if (entry.srtFile) {
      const srtPath = join(this.dir, entry.srtFile);
      rmSync(srtPath, { force: true });
    }
    if (entry.transcriptDataFile) {
      const transcriptDataPath = join(this.dir, entry.transcriptDataFile);
      rmSync(transcriptDataPath, { force: true });
    }
    if (entry.transcriptFile) {
      const transcriptPath = join(this.dir, entry.transcriptFile);
      rmSync(transcriptPath, { force: true });
    }
    if (entry.summaryFile) {
      const summaryPath = join(this.dir, entry.summaryFile);
      rmSync(summaryPath, { force: true });
    }

    if (opts?.localOnly) {
      // 保留索引条目，清除文件引用
      entry.audioFile = undefined;
      entry.srtFile = undefined;
      entry.transcriptDataFile = undefined;
      entry.transcriptFile = undefined;
      entry.summaryFile = undefined;
      entry.updatedAt = new Date().toISOString();
      this.saveIndex();
      this.logger.info(`录音本地文件已删除（保留索引）: ${recordingId}`);
    } else {
      // 从索引中移除
      this.index.recordings = this.index.recordings.filter(
        (r) => r.id !== recordingId,
      );
      this.saveIndex();
      this.logger.info(`录音已完整删除: ${recordingId}`);
    }
    return true;
  }

  // ─── Helpers ───

  /**
   * 生成音频文件名。扩展名从 OSS URL 中提取，未提供时默认 .ogg（硬件录音格式）
   */
  buildAudioFilename(recordingId: string, ossUrl?: string): string {
    const ext = ossUrl ? extractAudioExt(ossUrl) : ".ogg";
    return `${recordingId}${ext}`;
  }

  /**
   * 生成打点文件名
   */
  buildSrtFilename(recordingId: string): string {
    return `${recordingId}.srt`;
  }

  /**
   * 生成转写 JSON 文件名
   */
  buildTranscriptDataFilename(recordingId: string): string {
    return buildTranscriptDataJsonFilename(recordingId);
  }

  /**
   * 生成转写文件名
   * @param summary 摘要标题（≤ 10 字）
   */
  buildTranscriptFilename(recordingId: string, summary: string): string {
    // 清理文件名中的非法字符
    const safeSummary = summary
      .replace(/[/\\:*?"<>|]/g, "")
      .trim()
      .slice(0, 20);
    return safeSummary
      ? `${recordingId}_${safeSummary}.md`
      : `${recordingId}.md`;
  }

  /**
   * 生成摘要文件名
   */
  buildSummaryFilename(recordingId: string): string {
    return `${recordingId}.md`;
  }

  /**
   * 获取音频文件的绝对路径。ossUrl 用于推断文件扩展名
   */
  getAudioFilePath(recordingId: string, ossUrl?: string): string {
    return join(this.audioDir, this.buildAudioFilename(recordingId, ossUrl));
  }

  /**
   * 获取打点文件的绝对路径
   */
  getSrtFilePath(recordingId: string): string {
    return join(this.audioDir, this.buildSrtFilename(recordingId));
  }

  /**
   * 获取转写 JSON 文件的绝对路径
   */
  getTranscriptDataFilePath(recordingId: string): string {
    return join(this.transcriptDataDir, this.buildTranscriptDataFilename(recordingId));
  }

  /**
   * 获取摘要文件的绝对路径
   */
  getSummaryFilePath(recordingId: string): string {
    return join(this.summariesDir, this.buildSummaryFilename(recordingId));
  }

  // ─── Persistence ───

  private loadIndex(): void {
    if (!existsSync(this.indexPath)) {
      this.index = { recordings: [] };
      return;
    }
    try {
      const raw = JSON.parse(readFileSync(this.indexPath, "utf-8"));
      if (raw && Array.isArray(raw.recordings)) {
        let needsRewrite = false;
        const normalized = raw.recordings
          .filter((entry: unknown) => entry && typeof entry === "object")
          .map((entry: any) => {
            const compacted: RecordingIndexEntry = {
              id: entry.id,
              metadata: entry.metadata,
              status: entry.status,
              ingestedAt: entry.ingestedAt,
              updatedAt: entry.updatedAt,
            };
            if (typeof entry.audioFile === "string") compacted.audioFile = entry.audioFile;
            if (typeof entry.srtFile === "string") compacted.srtFile = entry.srtFile;
            if (typeof entry.transcriptFile === "string") {
              compacted.transcriptFile = entry.transcriptFile;
            }
            let transcriptDoc = typeof entry.transcriptDataFile === "string"
              ? this.readRelativeTranscriptDocument(entry.transcriptDataFile)
              : undefined;
            if (typeof entry.transcriptDataFile === "string" && transcriptDoc) {
              compacted.transcriptDataFile = entry.transcriptDataFile;
            } else {
              const legacyTranscriptText = typeof entry.transcript === "string" && entry.transcript.trim()
                ? entry.transcript.trim()
                : compacted.transcriptFile
                  ? extractTranscriptContent(
                    this.readRelativeTextFile(compacted.transcriptFile) ?? "",
                  ) || undefined
                  : undefined;
              const legacyTitle = typeof entry.title === "string" && entry.title.trim()
                ? entry.title.trim()
                : deriveTitleFromTranscriptPath(compacted.transcriptFile, compacted.id);
              const legacySummary = typeof entry.summary === "string" && entry.summary.trim()
                ? entry.summary.trim()
                : undefined;

              if (legacyTranscriptText) {
                transcriptDoc = buildTranscriptDocument({
                  recordingId: compacted.id,
                  generatedAt: compacted.updatedAt,
                  source: {
                    provider: "legacy-markdown",
                  },
                  title: legacyTitle,
                  summary: legacySummary,
                  text: legacyTranscriptText,
                  segments: [],
                });
                const transcriptDataFilename = this.buildTranscriptDataFilename(compacted.id);
                writeFileSync(
                  join(this.transcriptDataDir, transcriptDataFilename),
                  JSON.stringify(transcriptDoc, null, 2),
                  "utf-8",
                );
                compacted.transcriptDataFile = `${TRANSCRIPT_DATA_DIR}/${transcriptDataFilename}`;
                needsRewrite = true;
              }
            }

            if (typeof entry.summaryFile === "string") {
              compacted.summaryFile = entry.summaryFile;
            } else if (typeof entry.summary === "string" && entry.summary.trim()) {
              const summaryFilename = this.buildSummaryFilename(entry.id);
              writeFileSync(
                join(this.summariesDir, summaryFilename),
                entry.summary.trim(),
                "utf-8",
              );
              compacted.summaryFile = `${SUMMARIES_DIR}/${summaryFilename}`;
              needsRewrite = true;
            } else {
              const summaryFromDocument = extractTranscriptSummaryFromDocument(transcriptDoc);
              if (summaryFromDocument) {
                const summaryFilename = this.buildSummaryFilename(entry.id);
                writeFileSync(
                  join(this.summariesDir, summaryFilename),
                  summaryFromDocument,
                  "utf-8",
                );
                compacted.summaryFile = `${SUMMARIES_DIR}/${summaryFilename}`;
                needsRewrite = true;
              }
            }
            if (typeof entry.title === "string" && entry.title.trim()) {
              compacted.title = entry.title.trim();
            } else {
              const titleFromDocument = extractTranscriptTitleFromDocument(transcriptDoc);
              const derivedTitle = titleFromDocument ?? deriveTitleFromTranscriptPath(
                compacted.transcriptFile,
                compacted.id,
              );
              if (derivedTitle) {
                compacted.title = derivedTitle;
                needsRewrite = true;
              }
            }
            if (typeof entry.lastError === "string" && entry.lastError.trim()) {
              compacted.lastError = entry.lastError.trim();
            }
            if (compacted.status === "transcribing") {
              compacted.status = "transcribe_failed";
              compacted.lastError = compacted.lastError
                ?? "转写任务已中断，请重新发起转写";
              compacted.updatedAt = new Date().toISOString();
              needsRewrite = true;
            }
            return compacted;
          });
        const hadLargeFields = raw.recordings.some(
          (entry: any) => entry && typeof entry === "object" && ("transcript" in entry || "summary" in entry),
        );
        this.index = { recordings: normalized };
        if (hadLargeFields || needsRewrite) {
          this.saveIndex();
        }
      } else {
        this.index = { recordings: [] };
      }
    } catch {
      this.logger.warn(`录音索引文件损坏，已重建: ${this.indexPath}`);
      this.index = { recordings: [] };
    }
  }

  private readRelativeTextFile(relativePath: string): string | undefined {
    try {
      return readFileSync(join(this.dir, relativePath), "utf-8");
    } catch {
      return undefined;
    }
  }

  private readRelativeTranscriptDocument(relativePath: string) {
    const raw = this.readRelativeTextFile(relativePath);
    if (!raw) {
      return undefined;
    }
    return parseTranscriptDocument(raw);
  }

  private saveIndex(): void {
    writeFileSync(
      this.indexPath,
      JSON.stringify(this.index, null, 2),
      "utf-8",
    );
  }

  async close(): Promise<void> {
    // 当前无需额外清理
  }
}

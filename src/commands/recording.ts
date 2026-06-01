/**
 * recording service —— list / status / storage-path / setup-asr + +latest。
 * 查询纯读磁盘（🟢）；setup-asr 写 asr-config.json（🟢）。
 */
import { createReadStream, existsSync, readFileSync, statSync, watch } from "node:fs";
import { join } from "node:path";
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { ensureDir, writeJsonFile } from "../fs-utils.js";
import { ask, isInteractive } from "../prompt.js";
import { readRecordingIndex, type CliRecordingEntry } from "../shared.js";

function readIndex(ctx: CliContext): CliRecordingEntry[] {
  if (!existsSync(ctx.paths.recordings)) return [];
  return readRecordingIndex(ctx.paths.recordings);
}

function toListItem(r: CliRecordingEntry) {
  return {
    id: r.id,
    clientLabel: r.clientLabel ?? "legacy",
    name: r.metadata.name,
    duration_sec: r.metadata.duration_sec,
    status: r.status,
    file_size_bytes: r.metadata.file_size_bytes,
    has_audio: Boolean(r.audioFile),
    has_transcript: Boolean(r.transcriptFile),
    created_at: r.metadata.created_at,
    updated_at: r.updatedAt,
    error: r.lastError ?? null,
  };
}

export function recordingList(
  ctx: CliContext,
  _args: unknown[],
  opts: { status?: string; client?: string },
): unknown {
  let recordings = readIndex(ctx);
  if (opts.status) recordings = recordings.filter((r) => r.status === opts.status);
  if (opts.client && opts.client !== "all") {
    recordings = recordings.filter((r) => (r.clientLabel ?? "legacy") === opts.client);
  }
  return { ok: true, total: recordings.length, recordings: recordings.map(toListItem) };
}

export function recordingStatus(ctx: CliContext, args: unknown[]): unknown {
  const [id] = args as [string];
  const entry = readIndex(ctx).find((r) => r.id === id);
  if (!entry) {
    throw new YoooclawError("YOOOCLAW_NOT_FOUND", `录音不存在：${id}`);
  }
  return {
    ok: true,
    recording: {
      id: entry.id,
      clientLabel: entry.clientLabel ?? "legacy",
      name: entry.metadata.name,
      duration_sec: entry.metadata.duration_sec,
      file_size_bytes: entry.metadata.file_size_bytes,
      status: entry.status,
      created_at: entry.metadata.created_at,
      location: (entry.metadata as Record<string, unknown>).location ?? null,
      markers: (entry.metadata as Record<string, unknown>).markers ?? [],
      audioFile: entry.audioFile ?? null,
      srtFile: entry.srtFile ?? null,
      transcriptDataFile: entry.transcriptDataFile ?? null,
      transcriptFile: entry.transcriptFile ?? null,
      summaryFile: entry.summaryFile ?? null,
      title: entry.title ?? null,
      error: entry.lastError ?? null,
      ingestedAt: entry.ingestedAt,
      updatedAt: entry.updatedAt,
    },
  };
}

export function recordingStoragePath(ctx: CliContext): unknown {
  return { ok: true, path: ctx.paths.recordings };
}

// ── 录音状态事件流（由 daemon 写到 recordings/state/events.jsonl）──

interface EventRecord {
  ts: string;
  recordingId: string;
  transfer_status: string;
  error?: string;
  [k: string]: unknown;
}

interface RecordingEventsOpts {
  id?: string;
  since?: string;
  watch?: boolean;
  limit?: string;
}

const DURATION_UNITS: Record<string, number> = {
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
};

function parseSince(input: string | undefined): number | undefined {
  if (!input) return undefined;
  const m = /^(\d+)([smhd])$/.exec(input.trim());
  if (!m) {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      `--since 格式应为 <数字><单位>，单位 s/m/h/d（收到 "${input}"）`,
    );
  }
  return Date.now() - Number(m[1]) * DURATION_UNITS[m[2]];
}

function eventsPath(ctx: CliContext): string {
  return join(ctx.paths.recordings, "state", "events.jsonl");
}

function readEventsFile(path: string): EventRecord[] {
  if (!existsSync(path)) return [];
  const raw = readFileSync(path, "utf-8");
  const out: EventRecord[] = [];
  for (const line of raw.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    try {
      out.push(JSON.parse(t) as EventRecord);
    } catch {
      // 损坏行跳过
    }
  }
  return out;
}

function filterEvents(
  events: EventRecord[],
  opts: { id?: string; sinceMs?: number; limit: number },
): EventRecord[] {
  let out = events;
  if (opts.id) out = out.filter((e) => e.recordingId === opts.id);
  if (opts.sinceMs !== undefined) {
    out = out.filter((e) => {
      const ts = Date.parse(e.ts);
      return Number.isFinite(ts) && ts >= opts.sinceMs!;
    });
  }
  if (out.length > opts.limit) out = out.slice(-opts.limit);
  return out;
}

export async function recordingEvents(
  ctx: CliContext,
  _args: unknown[],
  opts: RecordingEventsOpts,
): Promise<unknown> {
  const path = eventsPath(ctx);
  const limit = Number(opts.limit ?? "200");
  const sinceMs = parseSince(opts.since);
  const initial = filterEvents(readEventsFile(path), { id: opts.id, sinceMs, limit });

  if (!opts.watch) {
    return { ok: true, path, total: initial.length, events: initial };
  }

  // --watch：先打印历史，再 tail。直接 stdout 行式输出（脱离命令 JSON 包装）。
  for (const e of initial) {
    process.stdout.write(JSON.stringify(e) + "\n");
  }
  await tailEvents(path, opts.id, (e) => {
    process.stdout.write(JSON.stringify(e) + "\n");
  });
  // tail 永不返回；写在这里只是给类型系统看
  return { ok: true, path, total: initial.length };
}

function tailEvents(
  path: string,
  filterId: string | undefined,
  onEvent: (e: EventRecord) => void,
): Promise<never> {
  return new Promise<never>(() => {
    let offset = existsSync(path) ? statSync(path).size : 0;
    let buffer = "";
    const drain = (): void => {
      if (!existsSync(path)) return;
      const size = statSync(path).size;
      if (size < offset) {
        // 文件被截断/重建，从头读
        offset = 0;
        buffer = "";
      }
      if (size === offset) return;
      const stream = createReadStream(path, { start: offset, end: size - 1, encoding: "utf-8" });
      stream.on("data", (chunk) => (buffer += chunk));
      stream.on("end", () => {
        offset = size;
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";
        for (const line of lines) {
          const t = line.trim();
          if (!t) continue;
          try {
            const e = JSON.parse(t) as EventRecord;
            if (!filterId || e.recordingId === filterId) onEvent(e);
          } catch {
            // skip
          }
        }
      });
    };
    drain();
    // 监听目录避免 watch 在文件不存在时报错
    const dir = join(path, "..");
    watch(dir, () => drain());
  });
}

export function recordingLatest(ctx: CliContext): unknown {
  const recordings = readIndex(ctx).sort(
    (a, b) => Date.parse(b.metadata.created_at) - Date.parse(a.metadata.created_at),
  );
  if (recordings.length === 0) {
    return { ok: true, recording: null };
  }
  return recordingStatus(ctx, [recordings[0].id]);
}

interface SetupAsrOpts {
  mode?: string;
  apiKey?: string;
  endpoint?: string;
  language?: string;
  model?: string;
  nonInteractive?: boolean;
}

/**
 * 写出与 phone-notifications/types.ts 的 AsrConfig 完全兼容的配置：
 *   { mode: "api" | "local", api?: {...}, local?: {...} }
 *
 * api 模式留空 apiKey 时，daemon 跑录音同步会回退到 account 级 ock- key
 * （resolveApiKey），所以普通用户 `yc recording setup-asr --non-interactive` 即可开箱。
 */
export async function recordingSetupAsr(
  ctx: CliContext,
  _args: unknown[],
  opts: SetupAsrOpts,
): Promise<unknown> {
  let mode = (opts.mode ?? "api").toLowerCase();
  let apiKey = opts.apiKey;
  let endpoint = opts.endpoint;
  let language = opts.language;
  let model = opts.model;

  if (!opts.nonInteractive && isInteractive()) {
    mode = (await ask("ASR mode（api / local）", mode)).toLowerCase();
    if (mode === "api") {
      apiKey = await ask("API key（留空则使用 account ock- key）", apiKey ?? "");
      endpoint = await ask("model-proxy endpoint（留空走默认）", endpoint ?? "");
      language = await ask("语言提示（zh / en / auto）", language ?? "auto");
    } else if (mode === "local") {
      model = await ask("Whisper 模型（留空走推荐值）", model ?? "");
    }
  }

  if (mode !== "api" && mode !== "local") {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      `--mode 必须是 api 或 local（收到 "${mode}"）`,
    );
  }

  const config = buildAsrConfig(mode, { apiKey, endpoint, language, model });

  ensureDir(ctx.paths.recordings);
  const path = join(ctx.paths.recordings, "asr-config.json");
  writeJsonFile(path, config);
  return {
    ok: true,
    path,
    mode,
    keyConfigured: mode === "api" ? Boolean(apiKey) || "fallback-to-account" : "n/a",
  };
}

function buildAsrConfig(
  mode: "api" | "local",
  fields: { apiKey?: string; endpoint?: string; language?: string; model?: string },
): Record<string, unknown> {
  if (mode === "api") {
    const api: Record<string, unknown> = {};
    if (fields.apiKey) api.apiKey = fields.apiKey;
    if (fields.endpoint) api.endpoint = fields.endpoint;
    if (fields.language) api.language = fields.language;
    return Object.keys(api).length > 0 ? { mode, api } : { mode };
  }
  const local: Record<string, unknown> = {};
  if (fields.model) local.model = fields.model;
  return Object.keys(local).length > 0 ? { mode, local } : { mode };
}

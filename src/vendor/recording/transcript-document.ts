export const TRANSCRIPT_DOCUMENT_SCHEMA_VERSION = 1;

export interface TranscriptDocumentSource {
  provider: string;
  taskId?: string;
  requestId?: string;
  status?: string;
}

export interface TranscriptDocumentSegment {
  text: string;
  startMs?: number;
  endMs?: number;
  speakerId?: number;
}

export interface TranscriptDocumentContentItem {
  content: string;
  speakerId?: number;
  startTime?: number;
  endTime?: number;
}

export interface TranscriptDocumentNormalized {
  title?: string;
  category?: string;
  summary?: string;
  text: string;
  segments: TranscriptDocumentSegment[];
}

export interface TranscriptDocument {
  schemaVersion: number;
  recordingId: string;
  generatedAt: string;
  source: TranscriptDocumentSource;
  normalized: TranscriptDocumentNormalized;
  raw?: unknown;
}

export function buildTranscriptDataFilename(recordingId: string): string {
  return `${recordingId}.json`;
}

export function buildTranscriptDocument(params: {
  recordingId: string;
  generatedAt?: string;
  source: TranscriptDocumentSource;
  title?: string;
  category?: string;
  summary?: string;
  text?: string;
  segments?: TranscriptDocumentSegment[];
  raw?: unknown;
}): TranscriptDocument {
  const segments = normalizeSegments(params.segments);
  const text = normalizePossiblyEmptyText(params.text) ?? joinSegmentsText(segments) ?? "";

  return {
    schemaVersion: TRANSCRIPT_DOCUMENT_SCHEMA_VERSION,
    recordingId: params.recordingId,
    generatedAt: params.generatedAt ?? new Date().toISOString(),
    source: {
      provider: params.source.provider,
      taskId: normalizeOptionalText(params.source.taskId),
      requestId: normalizeOptionalText(params.source.requestId),
      status: normalizeOptionalText(params.source.status),
    },
    normalized: {
      title: normalizeOptionalText(params.title),
      category: normalizeOptionalText(params.category),
      summary: normalizePossiblyEmptyText(params.summary),
      text,
      segments,
    },
    raw: params.raw,
  };
}

export function parseTranscriptDocument(
  value: string | unknown,
): TranscriptDocument | undefined {
  const parsed = typeof value === "string"
    ? parseJson(value)
    : value;

  if (!parsed || typeof parsed !== "object") {
    return undefined;
  }

  const doc = parsed as Record<string, unknown>;
  const recordingId = normalizeOptionalText(doc.recordingId);
  const generatedAt = normalizeOptionalText(doc.generatedAt);
  const source = normalizeSource(doc.source);
  const normalized = normalizeDocumentBody(doc.normalized);

  if (!recordingId || !generatedAt || !source || !normalized) {
    return undefined;
  }

  return {
    schemaVersion:
      typeof doc.schemaVersion === "number"
        ? doc.schemaVersion
        : TRANSCRIPT_DOCUMENT_SCHEMA_VERSION,
    recordingId,
    generatedAt,
    source,
    normalized,
    raw: doc.raw,
  };
}

export function extractTranscriptTextFromDocument(
  doc: TranscriptDocument | undefined,
): string | undefined {
  if (!doc) return undefined;
  return normalizePossiblyEmptyText(doc.normalized.text)
    ?? joinSegmentsText(doc.normalized.segments);
}

export function extractTranscriptSummaryFromDocument(
  doc: TranscriptDocument | undefined,
): string | undefined {
  if (!doc) return undefined;
  return normalizePossiblyEmptyText(doc.normalized.summary);
}

export function extractTranscriptTitleFromDocument(
  doc: TranscriptDocument | undefined,
): string | undefined {
  if (!doc) return undefined;
  return normalizeOptionalText(doc.normalized.title);
}

export function extractSourceTextListFromDocument(
  doc: TranscriptDocument | undefined,
): TranscriptDocumentContentItem[] | undefined {
  if (!doc) return undefined;

  const fromNormalized = doc.normalized.segments
    .map((segment) => ({
      content: segment.text,
      speakerId: segment.speakerId,
      startTime: segment.startMs,
      endTime: segment.endMs,
    }))
    .filter((segment) => normalizeOptionalText(segment.content));

  if (fromNormalized.length > 0) {
    return fromNormalized;
  }

  const rawList = extractRawSourceTextList(doc.raw);
  if (rawList.length > 0) {
    return rawList;
  }

  const normalizedText = normalizeOptionalText(doc.normalized.text);
  return normalizedText
    ? [{ content: normalizedText }]
    : undefined;
}

function parseJson(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

function normalizeSource(value: unknown): TranscriptDocumentSource | undefined {
  if (!value || typeof value !== "object") {
    return undefined;
  }

  const source = value as Record<string, unknown>;
  const provider = normalizeOptionalText(source.provider);
  if (!provider) {
    return undefined;
  }

  return {
    provider,
    taskId: normalizeOptionalText(source.taskId),
    requestId: normalizeOptionalText(source.requestId),
    status: normalizeOptionalText(source.status),
  };
}

function normalizeDocumentBody(
  value: unknown,
): TranscriptDocumentNormalized | undefined {
  if (!value || typeof value !== "object") {
    return undefined;
  }

  const normalized = value as Record<string, unknown>;
  const segments = normalizeSegments(normalized.segments);
  const text = normalizePossiblyEmptyText(normalized.text) ?? joinSegmentsText(segments);
  if (text === undefined) {
    return undefined;
  }

  return {
    title: normalizeOptionalText(normalized.title),
    category: normalizeOptionalText(normalized.category),
    summary: normalizePossiblyEmptyText(normalized.summary),
    text,
    segments,
  };
}

function normalizeSegments(value: unknown): TranscriptDocumentSegment[] {
  if (!Array.isArray(value)) {
    return [];
  }

  return value
    .map((item) => normalizeSegment(item))
    .filter((item): item is TranscriptDocumentSegment => !!item);
}

function normalizeSegment(value: unknown): TranscriptDocumentSegment | undefined {
  if (!value || typeof value !== "object") {
    return undefined;
  }

  const segment = value as Record<string, unknown>;
  const text = normalizeOptionalText(segment.text);
  if (!text) {
    return undefined;
  }

  return {
    text,
    startMs: normalizeOptionalNumber(segment.startMs),
    endMs: normalizeOptionalNumber(segment.endMs),
    speakerId: normalizeOptionalInteger(segment.speakerId),
  };
}

function joinSegmentsText(
  segments: TranscriptDocumentSegment[],
): string | undefined {
  const parts = segments
    .map((segment) => normalizeOptionalText(segment.text))
    .filter((segment): segment is string => !!segment);

  if (parts.length === 0) {
    return undefined;
  }

  return parts.join("\n\n");
}

function extractRawSourceTextList(raw: unknown): TranscriptDocumentContentItem[] {
  if (!raw || typeof raw !== "object") {
    return [];
  }

  const recordResult = (raw as { recordResult?: unknown }).recordResult;
  if (!recordResult || typeof recordResult !== "object") {
    return [];
  }

  const sourceTextList = (recordResult as { sourceTextList?: unknown }).sourceTextList;
  if (!Array.isArray(sourceTextList)) {
    return [];
  }

  return sourceTextList
    .flatMap((item) => {
      if (!item || typeof item !== "object") {
        return [];
      }

      const value = item as Record<string, unknown>;
      const content = normalizeOptionalText(value.content);
      if (!content) {
        return [];
      }

      return [{
        content,
        speakerId: normalizeOptionalInteger(value.speakerId),
        startTime: normalizeOptionalNumber(value.startTime),
        endTime: normalizeOptionalNumber(value.endTime),
      }];
    });
}

function normalizeOptionalText(value: unknown): string | undefined {
  return typeof value === "string" && value.trim()
    ? value.trim()
    : undefined;
}

function normalizePossiblyEmptyText(value: unknown): string | undefined {
  return typeof value === "string"
    ? value.trim()
    : undefined;
}

function normalizeOptionalNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}

function normalizeOptionalInteger(value: unknown): number | undefined {
  return Number.isInteger(value)
    ? Number(value)
    : undefined;
}

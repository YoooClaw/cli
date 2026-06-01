import { appendFileSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { PLUGIN_VERSION } from "./version.js";

export interface Logger {
  info: (msg: string) => void;
  warn: (msg: string) => void;
  error: (msg: string) => void;
}

const DEFAULT_LOG_RETENTION_DAYS = 30;
const DAY_MS = 24 * 60 * 60 * 1000;

export interface PluginFileLoggerOptions {
  version?: string;
  retentionDays?: number;
}

const REDACTED = "[redacted]";
const REDACTED_SECRET = "[redacted-secret]";
const REDACTED_URL = "[redacted-url]";

const SECRET_KEYS = [
  "apiKey",
  "api_key",
  "appKey",
  "authorization",
  "password",
  "secret",
  "token",
  "accessToken",
  "refreshToken",
  "gatewayToken",
  "gatewayPassword",
  "apnsToken",
  "deviceToken",
  "X-Api-Key-Id",
  "x-api-key-id",
  "x-openclaw-password",
];

const USER_TEXT_KEYS = [
  "accountId",
  "body",
  "content",
  "conversationName",
  "deviceId",
  "loginAccount",
  "metadata",
  "name",
  "raw",
  "rawResponse",
  "reason",
  "recordResult",
  "recordingName",
  "resBody",
  "response",
  "senderName",
  "sourceText",
  "sourceTextList",
  "summary",
  "summaryResult",
  "summaryText",
  "text",
  "title",
  "transcript",
  "transcriptData",
  "userId",
];

const USER_URL_KEYS = [
  "audio",
  "audioOssUrl",
  "fileUrl",
  "oss_audio_url",
  "oss_srt_url",
  "srt",
  "url",
];

const SENSITIVE_QUERY_KEYS = [
  "access_token",
  "api_key",
  "apikey",
  "authorization",
  "fileUrl",
  "password",
  "refresh_token",
  "secret",
  "signature",
  "token",
  "x-api-key-id",
  "x-oss-security-token",
];

export function isBetaPluginVersion(version = PLUGIN_VERSION): boolean {
  return /\bbeta\b/i.test(version);
}

export function createVersionAwareLogger(
  upstream: Logger,
  options: Pick<PluginFileLoggerOptions, "version"> = {},
): Logger {
  if (isBetaPluginVersion(options.version ?? PLUGIN_VERSION)) {
    return upstream;
  }

  return {
    info(msg: string) {
      upstream.info(redactLogMessage(msg));
    },
    warn(msg: string) {
      upstream.warn(redactLogMessage(msg));
    },
    error(msg: string) {
      upstream.error(redactLogMessage(msg));
    },
  };
}

/**
 * 插件级文件 Logger。
 * 在调用原始 api.logger 的同时，把日志追加写入
 * `<stateDir>/plugins/phone-notifications/logs/YYYY-MM-DD.log`。
 * beta 版本保留原始日志；正式版保留 INFO，并对日志里的用户数据做兜底脱敏。
 */
export class PluginFileLogger implements Logger {
  private readonly logsDir: string;
  private readonly redactLogs: boolean;
  private readonly retentionDays: number;
  private lastPruneDateKey: string | null = null;

  constructor(
    private readonly upstream: Logger,
    stateDir: string,
    options: PluginFileLoggerOptions = {},
  ) {
    this.logsDir = join(stateDir, "plugins", "phone-notifications", "logs");
    this.redactLogs = !isBetaPluginVersion(options.version ?? PLUGIN_VERSION);
    this.retentionDays =
      Number.isFinite(options.retentionDays) && (options.retentionDays ?? 0) > 0
        ? options.retentionDays!
        : DEFAULT_LOG_RETENTION_DAYS;

    mkdirSync(this.logsDir, { recursive: true });
    const now = new Date();
    this.pruneExpiredLogs(now);
    this.lastPruneDateKey = formatDate(now);
  }

  info(msg: string): void {
    this.prepareLogDate(new Date());
    const safeMsg = this.formatLogMessage(msg);
    this.upstream.info(safeMsg);
    this.append("INFO", safeMsg);
  }

  warn(msg: string): void {
    const safeMsg = this.formatLogMessage(msg);
    this.upstream.warn(safeMsg);
    this.append("WARN", safeMsg);
  }

  error(msg: string): void {
    const safeMsg = this.formatLogMessage(msg);
    this.upstream.error(safeMsg);
    this.append("ERROR", safeMsg);
  }

  private formatLogMessage(msg: string): string {
    return this.redactLogs ? redactLogMessage(msg) : msg;
  }

  private append(level: string, msg: string): void {
    const now = new Date();
    const dateKey = this.prepareLogDate(now);
    const time = formatLocalTimestamp(now);
    const line = `${time} [${level}] ${msg}\n`;
    try {
      appendFileSync(join(this.logsDir, `${dateKey}.log`), line);
    } catch {
      // 写文件失败不影响主流程
    }
  }

  private prepareLogDate(now: Date): string {
    const dateKey = formatDate(now);
    if (this.lastPruneDateKey !== dateKey) {
      this.pruneExpiredLogs(now);
      this.lastPruneDateKey = dateKey;
    }
    return dateKey;
  }

  private pruneExpiredLogs(now: Date): void {
    const cutoffMs = now.getTime() - this.retentionDays * DAY_MS;
    const cutoffDate = formatDate(new Date(cutoffMs));

    try {
      for (const entry of readdirSync(this.logsDir, { withFileTypes: true })) {
        if (!entry.isFile()) continue;
        const match = /^(\d{4}-\d{2}-\d{2})\.log$/.exec(entry.name);
        if (match && match[1] < cutoffDate) {
          rmSync(join(this.logsDir, entry.name), { force: true });
        }
      }
    } catch {
      // 清理失败不影响主流程
    }
  }
}

export function redactLogMessage(msg: string): string {
  let redacted = String(msg);

  redacted = redactQuotedObjectFields(redacted, SECRET_KEYS, REDACTED_SECRET);
  redacted = redactQuotedObjectFields(redacted, USER_TEXT_KEYS, REDACTED);
  redacted = redactQuotedObjectFields(redacted, USER_URL_KEYS, REDACTED_URL);

  redacted = redactStructuredKeyValueFields(
    redacted,
    SECRET_KEYS,
    REDACTED_SECRET,
  );
  redacted = redactStructuredKeyValueFields(redacted, USER_TEXT_KEYS, REDACTED);
  redacted = redactStructuredKeyValueFields(redacted, USER_URL_KEYS, REDACTED_URL);

  redacted = redactKeyValueFields(redacted, SECRET_KEYS, REDACTED_SECRET);
  redacted = redactKeyValueFields(redacted, USER_TEXT_KEYS, REDACTED);
  redacted = redactKeyValueFields(redacted, USER_URL_KEYS, REDACTED_URL);

  redacted = redactColonTextFields(redacted, SECRET_KEYS, REDACTED_SECRET);
  redacted = redactColonTextFields(redacted, USER_TEXT_KEYS, REDACTED);
  redacted = redactQueryParams(redacted, SENSITIVE_QUERY_KEYS);
  redacted = redactBearerTokens(redacted);
  redacted = redactLikelyUserUrls(redacted);
  redacted = redactEmails(redacted);
  redacted = redactPhoneNumbers(redacted);
  redacted = redactJwtTokens(redacted);
  redacted = redactLongHexTokens(redacted);

  return redacted;
}

function redactQuotedObjectFields(
  input: string,
  keys: string[],
  placeholder: string,
): string {
  const keyPattern = buildKeyPattern(keys);
  return input.replace(
    new RegExp(
      `(["'])(${keyPattern})\\1\\s*:\\s*(?:"(?:\\\\.|[^"\\\\])*"|'(?:\\\\.|[^'\\\\])*'|[^,}\\]]+)`,
      "gi",
    ),
    (_match, quote: string, key: string) =>
      `${quote}${key}${quote}: "${placeholder}"`,
  );
}

function redactKeyValueFields(
  input: string,
  keys: string[],
  placeholder: string,
): string {
  const keyPattern = buildKeyPattern(keys);
  return input.replace(
    new RegExp(
      `(?<![?&])\\b(${keyPattern})\\s*=\\s*(?:Bearer\\s+[^,\\s)]+|"(?:\\\\.|[^"\\\\])*"|'(?:\\\\.|[^'\\\\])*'|[^,\\s)]+)`,
      "gi",
    ),
    (_match, key: string) => `${key}=${placeholder}`,
  );
}

function redactStructuredKeyValueFields(
  input: string,
  keys: string[],
  placeholder: string,
): string {
  const keyPattern = buildKeyPattern(keys);
  const regex = new RegExp(`\\b(${keyPattern})\\s*=\\s*([\\[{])`, "gi");
  let result = "";
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = regex.exec(input))) {
    const valueStart = regex.lastIndex - 1;
    const valueEnd = findStructuredValueEnd(input, valueStart);
    if (valueEnd === null) {
      continue;
    }

    result += input.slice(lastIndex, match.index);
    result += `${match[1]}=${placeholder}`;
    lastIndex = valueEnd;
    regex.lastIndex = valueEnd;
  }

  if (lastIndex === 0) {
    return input;
  }

  return result + input.slice(lastIndex);
}

function findStructuredValueEnd(input: string, start: number): number | null {
  const open = input[start];
  const close = open === "{" ? "}" : "]";
  const stack: string[] = [close];
  let quote: string | null = null;
  let escaped = false;

  for (let i = start + 1; i < input.length; i++) {
    const ch = input[i];

    if (quote) {
      if (escaped) {
        escaped = false;
      } else if (ch === "\\") {
        escaped = true;
      } else if (ch === quote) {
        quote = null;
      }
      continue;
    }

    if (ch === "\"" || ch === "'") {
      quote = ch;
      continue;
    }

    if (ch === "{" || ch === "[") {
      stack.push(ch === "{" ? "}" : "]");
      continue;
    }

    if (ch === stack[stack.length - 1]) {
      stack.pop();
      if (stack.length === 0) {
        return i + 1;
      }
    }
  }

  return null;
}

function redactColonTextFields(
  input: string,
  keys: string[],
  placeholder: string,
): string {
  const keyPattern = buildKeyPattern(keys);
  return input.replace(
    new RegExp(`\\b(${keyPattern})\\s*:\\s*([^,\\n]+)`, "gi"),
    (_match, key: string) => `${key}: ${placeholder}`,
  );
}

function redactQueryParams(input: string, keys: string[]): string {
  const keyPattern = buildKeyPattern(keys);
  return input.replace(
    new RegExp(`([?&](${keyPattern})=)([^&#\\s]+)`, "gi"),
    (_match, prefix: string) => `${prefix}${encodeURIComponent(REDACTED)}`,
  );
}

function redactBearerTokens(input: string): string {
  return input.replace(
    /\b(Bearer\s+)[A-Za-z0-9._~+/=-]+/gi,
    `$1${REDACTED_SECRET}`,
  );
}

function redactJwtTokens(input: string): string {
  return input.replace(
    /\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g,
    REDACTED_SECRET,
  );
}

function redactLongHexTokens(input: string): string {
  return input.replace(/\b[a-f0-9]{32,}\b/gi, REDACTED_SECRET);
}

function redactEmails(input: string): string {
  return input.replace(
    /\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/gi,
    "[redacted-email]",
  );
}

function redactPhoneNumbers(input: string): string {
  return input.replace(
    /\b1[3-9]\d{9}\b/g,
    (phone) => `${phone.slice(0, 3)}****${phone.slice(-4)}`,
  );
}

function redactLikelyUserUrls(input: string): string {
  return input.replace(/https?:\/\/[^\s"',)]+/gi, (rawUrl) =>
    redactUrl(rawUrl),
  );
}

function redactUrl(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    for (const key of Array.from(url.searchParams.keys())) {
      if (
        SENSITIVE_QUERY_KEYS.some(
          (sensitiveKey) => sensitiveKey.toLowerCase() === key.toLowerCase(),
        )
      ) {
        url.searchParams.set(key, REDACTED);
      }
    }

    if (shouldRedactUrlPath(url)) {
      return `${url.origin}/${REDACTED_URL}`;
    }

    return url.toString();
  } catch {
    return rawUrl;
  }
}

function shouldRedactUrlPath(url: URL): boolean {
  const host = url.hostname.toLowerCase();
  const path = decodeURIComponent(url.pathname).toLowerCase();
  if (
    /(^|\.)((oss|cos|s3|storage|cdn)[.-])/.test(host)
    || /(aliyuncs|myqcloud|amazonaws|oss|storage|cdn)/.test(host)
  ) {
    return true;
  }
  return (
    /\/(audio|avatar|feedback|log|logs|recording|recordings)\//.test(path)
    || /\.(aac|flac|json|m4a|md|mp3|ogg|opus|srt|wav|zip)$/i.test(path)
  );
}

function buildKeyPattern(keys: string[]): string {
  return keys.map(escapeRegExp).join("|");
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function formatDate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

export function formatLocalTimestamp(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  const ms = String(d.getMilliseconds()).padStart(3, "0");

  const offsetMinutes = -d.getTimezoneOffset();
  const sign = offsetMinutes >= 0 ? "+" : "-";
  const absOffsetMinutes = Math.abs(offsetMinutes);
  const offsetHours = String(Math.floor(absOffsetMinutes / 60)).padStart(2, "0");
  const offsetMins = String(absOffsetMinutes % 60).padStart(2, "0");

  return `${y}-${m}-${day}T${hh}:${mm}:${ss}.${ms}${sign}${offsetHours}:${offsetMins}`;
}

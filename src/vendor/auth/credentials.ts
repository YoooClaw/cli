import {
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  watch,
} from "node:fs";
import { dirname, basename } from "node:path";
import { resolveStateFile } from "../host.js";

interface Credentials {
  /**
   * 推荐：API Key 明文（例如 ock_xxx）
   */
  apiKey?: string;
  /**
   * CLI multi-key 共享格式。存在该字段时以它为准；空数组表示显式未配置 key。
   */
  apiKeys?: ApiKeyEntry[];
  /**
   * 兼容旧字段：历史上这里存的是 token；现在等价于 apiKey
   */
  token?: string;
  /**
   * 当前环境（development / production），由 ntf env switch 写入
   */
  env?: string;
}

interface ApiKeyEntry {
  label?: string;
  key?: string;
  default?: boolean;
  createdAt?: string;
}

const API_KEY_ENV = "YOOOCLAW_API_KEY";

export function credentialsPath(): string {
  return resolveStateFile("credentials.json");
}

export function readCredentials(): Credentials {
  const path = credentialsPath();
  if (!existsSync(path)) return {};
  try {
    return JSON.parse(readFileSync(path, "utf-8"));
  } catch {
    return {};
  }
}

export function writeCredentials(creds: Credentials): void {
  const path = credentialsPath();
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, JSON.stringify(creds, null, 2), {
    encoding: "utf-8",
    mode: 0o600,
  });
}

function normalizedKey(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function loadApiKeyFromMulti(creds: Credentials): string | undefined {
  if (!Array.isArray(creds.apiKeys)) return undefined;
  const entries = creds.apiKeys
    .map((entry) => ({
      key: normalizedKey(entry?.key),
      default: entry?.default === true,
    }))
    .filter((entry): entry is { key: string; default: boolean } => Boolean(entry.key));
  return entries.find((entry) => entry.default)?.key ?? entries[0]?.key;
}

export function loadApiKey(): string | undefined {
  const env = normalizedKey(process.env[API_KEY_ENV]);
  if (env) return env;

  const creds = readCredentials();
  if (Array.isArray(creds.apiKeys)) {
    return loadApiKeyFromMulti(creds);
  }
  return normalizedKey(creds.apiKey) ?? normalizedKey(creds.token);
}

export function writeSingleApiKey(apiKey: string): void {
  const creds = readCredentials();
  delete creds.apiKeys;
  writeCredentials({ ...creds, apiKey, token: undefined });
}

export function clearApiKeys(): void {
  const creds = readCredentials();
  delete creds.apiKey;
  delete creds.apiKeys;
  delete creds.token;
  writeCredentials(creds);
}

export function requireApiKey(): string {
  const apiKey = loadApiKey();
  if (!apiKey) {
    throw new Error(
      `API Key 未设置，请先写入 ${credentialsPath()}，或通过宿主 CLI 执行 ntf auth set-api-key <apiKey>`,
    );
  }
  return apiKey;
}

// ---- Backward-compatible exports (旧代码仍可编译；语义等价于 apiKey) ----
export function loadToken(): string | undefined {
  return loadApiKey();
}

export function requireToken(): string {
  return requireApiKey();
}

/** 监听 credentials 文件变化（如 set-token 后）触发 callback；返回取消监听的函数 */
export function watchCredentials(onChange: () => void): () => void {
  const path = credentialsPath();
  const dir = dirname(path);
  const filename = basename(path);
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;
  const delayMs = 200;

  const listener = (_event: string, changedName: string | null) => {
    if (!changedName || (changedName !== filename && !changedName.endsWith(filename))) return;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      debounceTimer = null;
      onChange();
    }, delayMs);
  };

  let watcher: ReturnType<typeof watch> | null = null;
  try {
    watcher = watch(dir, { persistent: false }, listener);
  } catch {
    // 目录可能不存在
  }

  return () => {
    if (debounceTimer) clearTimeout(debounceTimer);
    watcher?.close();
  };
}

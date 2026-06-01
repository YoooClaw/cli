/**
 * 凭据存取 —— account 级 api-key（共享）与 instance 级 gateway token（per-profile）。
 *
 * api-key 分层解析（PRD「凭据解析顺序与插件共享」），命中即停：
 *   1. env  YOOOCLAW_API_KEY     —— 显式覆盖
 *   2. keychain yoooclaw/api-key —— 可选加固
 *   3. file ~/.yoooclaw/credentials.json#apiKey —— 默认共享底座（与插件共用）
 */
import { randomBytes } from "node:crypto";
import { sharedCredentialsPath } from "../paths.js";
import { readJsonFile, writeJsonFile, SECRET_FILE_MODE } from "../fs-utils.js";
import { keychainAvailable, keychainGet, keychainSet } from "./keychain.js";
import { resolveRef, writeRef, type ResolvedRef } from "./refs.js";
import type { YoooclawConfig } from "../config/schema.js";
import { ErrorCode, YoooclawError } from "../errors.js";

export const API_KEY_ENV = "YOOOCLAW_API_KEY";
export const KEYCHAIN_SERVICE = "yoooclaw";
export const KEYCHAIN_API_KEY_ACCOUNT = "api-key";
export const API_KEY_FIELD = "apiKey";
export const API_KEYS_FIELD = "apiKeys";

export interface ApiKeyResolution {
  value?: string;
  source: ApiKeyEntry["source"] | "none";
  location?: string;
  label?: string;
}

export interface ApiKeyEntry {
  label: string;
  key: string;
  default: boolean;
  source: "env" | "keychain" | "file";
  createdAt?: string;
}

export type CredentialMode =
  | "env-single"
  | "file-multi"
  | "keychain-single"
  | "legacy-file-single"
  | "none";

export interface CredentialSet {
  mode: CredentialMode;
  entries: ApiKeyEntry[];
  defaultEntry?: ApiKeyEntry;
  location?: string;
  legacyApiKeyPresent: boolean;
  shadowedKeychainPresent: boolean;
  warnings: string[];
}

export interface StoredApiKeyEntry {
  label: string;
  key: string;
  default?: boolean;
  createdAt?: string;
}

const RESERVED_LABELS = new Set(["all", "legacy", "env", "keychain", "local"]);
const LABEL_RE = /^[a-z0-9-]{1,32}$/;

function readSharedCredentials(): Record<string, unknown> {
  return readJsonFile<Record<string, unknown>>(sharedCredentialsPath()) ?? {};
}

function writeSharedCredentials(data: Record<string, unknown>): void {
  writeJsonFile(sharedCredentialsPath(), data, SECRET_FILE_MODE);
}

function normalizeStoredEntry(raw: unknown, warnings: string[]): StoredApiKeyEntry | null {
  if (!raw || typeof raw !== "object") {
    warnings.push("apiKeys[] 包含非对象条目，已跳过");
    return null;
  }
  const item = raw as Record<string, unknown>;
  const label = typeof item.label === "string" ? item.label.trim() : "";
  const key = typeof item.key === "string" ? item.key.trim() : "";
  if (!isValidApiKeyLabel(label)) {
    warnings.push(`apiKeys[] 包含非法 label: ${label || "<empty>"}，已跳过`);
    return null;
  }
  if (!key) {
    warnings.push(`apiKeys[] label=${label} 的 key 为空，已跳过`);
    return null;
  }
  return {
    label,
    key,
    default: item.default === true,
    createdAt: typeof item.createdAt === "string" ? item.createdAt : undefined,
  };
}

function normalizeStoredEntries(raw: unknown, warnings: string[]): StoredApiKeyEntry[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const entries: StoredApiKeyEntry[] = [];
  for (const item of raw) {
    const normalized = normalizeStoredEntry(item, warnings);
    if (!normalized) continue;
    if (seen.has(normalized.label)) {
      warnings.push(`apiKeys[] label=${normalized.label} 重复，已跳过后续条目`);
      continue;
    }
    seen.add(normalized.label);
    entries.push(normalized);
  }
  return entries;
}

function toRuntimeEntries(entries: StoredApiKeyEntry[]): ApiKeyEntry[] {
  const defaultIdx = entries.findIndex((entry) => entry.default === true);
  const effectiveDefaultIdx = entries.length === 0 ? -1 : defaultIdx >= 0 ? defaultIdx : 0;
  return entries.map((entry, idx) => ({
    label: entry.label,
    key: entry.key,
    default: idx === effectiveDefaultIdx,
    source: "file",
    createdAt: entry.createdAt,
  }));
}

function defaultEntry(entries: ApiKeyEntry[]): ApiKeyEntry | undefined {
  return entries.find((entry) => entry.default) ?? entries[0];
}

function keychainValue(): string | undefined {
  if (!keychainAvailable()) return undefined;
  const r = keychainGet(KEYCHAIN_SERVICE, KEYCHAIN_API_KEY_ACCOUNT);
  return r.value || undefined;
}

function hasOwn(data: Record<string, unknown>, field: string): boolean {
  return Object.prototype.hasOwnProperty.call(data, field);
}

export function isValidApiKeyLabel(label: string): boolean {
  return LABEL_RE.test(label) && !RESERVED_LABELS.has(label);
}

export function assertValidApiKeyLabel(label: string): void {
  if (!isValidApiKeyLabel(label)) {
    throw new YoooclawError(
      ErrorCode.APIKEY_LABEL_INVALID,
      "label 不合法：仅允许 [a-z0-9-]{1,32}，且不能使用保留字 all/legacy/env/keychain/local",
      { label },
    );
  }
}

function storedEntriesFromFile(data: Record<string, unknown>): StoredApiKeyEntry[] {
  const warnings: string[] = [];
  return normalizeStoredEntries(data[API_KEYS_FIELD], warnings);
}

function writeApiKeys(entries: StoredApiKeyEntry[], extra?: Record<string, unknown>): ApiKeyResolution {
  const data = { ...readSharedCredentials(), ...(extra ?? {}) };
  data[API_KEYS_FIELD] = entries.map((entry) => ({
    label: entry.label,
    key: entry.key,
    ...(entry.default ? { default: true } : {}),
    ...(entry.createdAt ? { createdAt: entry.createdAt } : {}),
  }));
  delete data[API_KEY_FIELD];
  writeSharedCredentials(data);
  const runtime = toRuntimeEntries(entries);
  const chosen = defaultEntry(runtime);
  return { value: chosen?.key, source: chosen?.source ?? "none", location: sharedCredentialsPath(), label: chosen?.label };
}

export function resolveApiKeyEntries(): CredentialSet {
  const env = process.env[API_KEY_ENV]?.trim();
  if (env) {
    const entry: ApiKeyEntry = { label: "env", key: env, default: true, source: "env" };
    return {
      mode: "env-single",
      entries: [entry],
      defaultEntry: entry,
      location: API_KEY_ENV,
      legacyApiKeyPresent: false,
      shadowedKeychainPresent: false,
      warnings: [],
    };
  }

  const file = sharedCredentialsPath();
  const data = readSharedCredentials();
  const legacy = typeof data[API_KEY_FIELD] === "string" && Boolean((data[API_KEY_FIELD] as string).trim());
  const kc = keychainValue();

  if (hasOwn(data, API_KEYS_FIELD)) {
    const warnings: string[] = [];
    const stored = normalizeStoredEntries(data[API_KEYS_FIELD], warnings);
    if (stored.length > 0 && !stored.some((entry) => entry.default)) {
      warnings.push(`apiKeys[] 未标记 default，运行时使用第一条 label=${stored[0].label}`);
    }
    const entries = toRuntimeEntries(stored);
    return {
      mode: "file-multi",
      entries,
      defaultEntry: defaultEntry(entries),
      location: file,
      legacyApiKeyPresent: legacy,
      shadowedKeychainPresent: Boolean(kc),
      warnings,
    };
  }

  if (kc) {
    const entry: ApiKeyEntry = { label: "keychain", key: kc, default: true, source: "keychain" };
    return {
      mode: "keychain-single",
      entries: [entry],
      defaultEntry: entry,
      location: `${KEYCHAIN_SERVICE}/${KEYCHAIN_API_KEY_ACCOUNT}`,
      legacyApiKeyPresent: legacy,
      shadowedKeychainPresent: false,
      warnings: [],
    };
  }

  const value = legacy ? (data[API_KEY_FIELD] as string).trim() : "";
  if (value) {
    const entry: ApiKeyEntry = { label: "default", key: value, default: true, source: "file" };
    return {
      mode: "legacy-file-single",
      entries: [entry],
      defaultEntry: entry,
      location: file,
      legacyApiKeyPresent: true,
      shadowedKeychainPresent: false,
      warnings: [],
    };
  }

  return {
    mode: "none",
    entries: [],
    location: file,
    legacyApiKeyPresent: false,
    shadowedKeychainPresent: false,
    warnings: [],
  };
}

/** 按分层顺序解析 account 级 api-key。 */
export function resolveApiKey(): ApiKeyResolution {
  const set = resolveApiKeyEntries();
  const entry = set.defaultEntry;
  if (!entry) return { source: "none", location: set.location };
  return { value: entry.key, source: entry.source, location: set.location, label: entry.label };
}

/** 写入 account 级 api-key。默认写共享文件；--keychain 时写 OS keychain。 */
export function setApiKey(key: string, useKeychain: boolean): ApiKeyResolution {
  const trimmed = key.trim();
  if (!trimmed) {
    throw new YoooclawError(ErrorCode.INVALID_ARGUMENT, "api-key 不能为空");
  }
  if (useKeychain) {
    const data = readSharedCredentials();
    if (hasOwn(data, API_KEYS_FIELD)) {
      throw new YoooclawError(
        ErrorCode.KEYCHAIN_MULTI_UNSUPPORTED,
        "multi-key 模式不支持 --keychain；请使用文件 apiKeys[] 管理多 key",
      );
    }
    if (!keychainSet(KEYCHAIN_SERVICE, KEYCHAIN_API_KEY_ACCOUNT, trimmed)) {
      throw new YoooclawError(
        ErrorCode.KEYCHAIN_UNAVAILABLE,
        "当前平台无法写入 keychain，请去掉 --keychain 改写共享文件",
      );
    }
    return { value: trimmed, source: "keychain", location: `${KEYCHAIN_SERVICE}/${KEYCHAIN_API_KEY_ACCOUNT}`, label: "keychain" };
  }
  const file = sharedCredentialsPath();
  const data = readSharedCredentials();
  if (hasOwn(data, API_KEYS_FIELD)) {
    const entries = storedEntriesFromFile(data);
    if (entries.length === 0) {
      return writeApiKeys([{ label: "default", key: trimmed, default: true, createdAt: new Date().toISOString() }]);
    }
    const idx = entries.findIndex((entry) => entry.default) >= 0
      ? entries.findIndex((entry) => entry.default)
      : 0;
    const next = entries.map((entry, entryIdx) => ({
      ...entry,
      key: entryIdx === idx ? trimmed : entry.key,
      default: entryIdx === idx,
    }));
    return writeApiKeys(next);
  }
  data[API_KEY_FIELD] = trimmed;
  writeJsonFile(file, data, SECRET_FILE_MODE);
  return { value: trimmed, source: "file", location: file, label: "default" };
}

export function addApiKey(
  key: string,
  label: string,
  opts: { makeDefault?: boolean; force?: boolean } = {},
): ApiKeyResolution {
  const trimmed = key.trim();
  if (!trimmed) {
    throw new YoooclawError(ErrorCode.INVALID_ARGUMENT, "api-key 不能为空");
  }
  assertValidApiKeyLabel(label);
  const data = readSharedCredentials();
  let entries: StoredApiKeyEntry[];
  if (hasOwn(data, API_KEYS_FIELD)) {
    entries = storedEntriesFromFile(data);
  } else {
    const legacy = typeof data[API_KEY_FIELD] === "string" ? data[API_KEY_FIELD].trim() : "";
    entries = legacy
      ? [{ label: "default", key: legacy, default: true, createdAt: new Date().toISOString() }]
      : [];
  }

  const existingIdx = entries.findIndex((entry) => entry.label === label);
  if (existingIdx >= 0 && !opts.force) {
    throw new YoooclawError(
      ErrorCode.APIKEY_LABEL_DUPLICATE,
      `api-key label 已存在：${label}`,
      { hint: "如需覆盖，追加 --force", label },
    );
  }

  const shouldDefault = opts.makeDefault || entries.length === 0;
  const nextEntry: StoredApiKeyEntry = {
    label,
    key: trimmed,
    default: shouldDefault,
    createdAt: existingIdx >= 0 ? entries[existingIdx].createdAt : new Date().toISOString(),
  };
  if (existingIdx >= 0) entries[existingIdx] = nextEntry;
  else entries.push(nextEntry);
  if (shouldDefault) {
    entries = entries.map((entry) => ({ ...entry, default: entry.label === label }));
  } else if (!entries.some((entry) => entry.default)) {
    entries[0] = { ...entries[0], default: true };
  }

  return writeApiKeys(entries);
}

export function removeApiKey(label: string): { result: ApiKeyResolution; removed: string; newDefault?: string } {
  assertValidApiKeyLabel(label);
  const data = readSharedCredentials();
  const entries = hasOwn(data, API_KEYS_FIELD)
    ? storedEntriesFromFile(data)
    : (() => {
        const legacy = typeof data[API_KEY_FIELD] === "string" ? data[API_KEY_FIELD].trim() : "";
        return legacy ? [{ label: "default", key: legacy, default: true, createdAt: new Date().toISOString() }] : [];
      })();
  const idx = entries.findIndex((entry) => entry.label === label);
  if (idx < 0) {
    throw new YoooclawError(ErrorCode.APIKEY_LABEL_NOT_FOUND, `api-key label 不存在：${label}`, { label });
  }
  const removedDefault = entries[idx].default === true;
  let next = entries.filter((entry) => entry.label !== label);
  if (next.length > 0 && (removedDefault || !next.some((entry) => entry.default))) {
    next = next.map((entry, entryIdx) => ({ ...entry, default: entryIdx === 0 }));
  }
  const result = writeApiKeys(next);
  return { result, removed: label, newDefault: result.label };
}

export function setDefaultApiKey(label: string): ApiKeyResolution {
  assertValidApiKeyLabel(label);
  const data = readSharedCredentials();
  const entries = hasOwn(data, API_KEYS_FIELD) ? storedEntriesFromFile(data) : [];
  const idx = entries.findIndex((entry) => entry.label === label);
  if (idx < 0) {
    throw new YoooclawError(ErrorCode.APIKEY_LABEL_NOT_FOUND, `api-key label 不存在：${label}`, { label });
  }
  return writeApiKeys(entries.map((entry) => ({ ...entry, default: entry.label === label })));
}

export function maskSecret(value: string | undefined): string | undefined {
  if (!value) return undefined;
  if (value.length <= 8) return "***";
  return `${value.slice(0, 4)}***${value.slice(-4)}`;
}

/** 解析当前 profile 的 gateway token（按 config.auth.tokenRef）。 */
export function resolveGatewayToken(config: YoooclawConfig): ResolvedRef {
  return resolveRef(config.auth.tokenRef);
}

/** 写入/轮换 gateway token（按 config.auth.tokenRef 的 scheme）。 */
export function writeGatewayToken(config: YoooclawConfig, value: string): ResolvedRef {
  return writeRef(config.auth.tokenRef, value);
}

/** 生成一个随机 token（hex）。 */
export function generateToken(bytes = 32): string {
  return randomBytes(bytes).toString("hex");
}

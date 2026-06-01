/**
 * config.json 读写 + 点号路径 get/set/unset + 敏感字段遮罩。
 */
import { existsSync } from "node:fs";
import type { ProfilePaths } from "../paths.js";
import { readJsonFile, writeJsonFile, CONFIG_FILE_MODE } from "../fs-utils.js";
import { YoooclawError } from "../errors.js";
import {
  defaultConfig,
  SECRET_REF_PATHS,
  type YoooclawConfig,
} from "./schema.js";

type PlainObject = Record<string, unknown>;

function isPlainObject(value: unknown): value is PlainObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** 深合并：用 source 覆盖 target（仅对象递归，数组/标量整体替换）。 */
export function deepMerge<T>(target: T, source: unknown): T {
  if (!isPlainObject(source)) return target;
  const out: PlainObject = isPlainObject(target) ? { ...target } : {};
  for (const [key, value] of Object.entries(source)) {
    const prev = out[key];
    out[key] = isPlainObject(value) && isPlainObject(prev)
      ? deepMerge(prev, value)
      : value;
  }
  return out as T;
}

export function configExists(paths: ProfilePaths): boolean {
  return existsSync(paths.config);
}

/** 读取 config；缺失字段用默认值补齐。文件不存在则返回纯默认。 */
export function loadConfig(paths: ProfilePaths): YoooclawConfig {
  const base = defaultConfig(paths.credentials);
  const stored = readJsonFile<Partial<YoooclawConfig>>(paths.config);
  if (!stored) return base;
  return deepMerge(base, stored);
}

/** 要求 config 必须已存在（如 daemon 启动场景），否则报错。 */
export function requireConfig(paths: ProfilePaths): YoooclawConfig {
  if (!configExists(paths)) {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `profile \`${paths.profile}\` 尚未初始化`,
      { hint: "先运行 yoooclaw config init", checkedPaths: [paths.config] },
    );
  }
  return loadConfig(paths);
}

export function saveConfig(paths: ProfilePaths, config: YoooclawConfig): void {
  writeJsonFile(paths.config, config, CONFIG_FILE_MODE);
}

/** 按点号路径取值。 */
export function getByPath(obj: unknown, path: string): unknown {
  const segments = path.split(".");
  let cursor: unknown = obj;
  for (const seg of segments) {
    if (!isPlainObject(cursor)) return undefined;
    cursor = cursor[seg];
  }
  return cursor;
}

/** 按点号路径设值（中间缺失对象自动创建）。返回新对象（原地修改 obj 的拷贝语义由调用方保证）。 */
export function setByPath(obj: PlainObject, path: string, value: unknown): void {
  const segments = path.split(".");
  let cursor: PlainObject = obj;
  for (let i = 0; i < segments.length - 1; i += 1) {
    const seg = segments[i];
    if (!isPlainObject(cursor[seg])) cursor[seg] = {};
    cursor = cursor[seg] as PlainObject;
  }
  cursor[segments.at(-1)!] = value;
}

/** 按点号路径删除；返回是否删除了字段。 */
export function unsetByPath(obj: PlainObject, path: string): boolean {
  const segments = path.split(".");
  let cursor: PlainObject = obj;
  for (let i = 0; i < segments.length - 1; i += 1) {
    const seg = segments[i];
    if (!isPlainObject(cursor[seg])) return false;
    cursor = cursor[seg] as PlainObject;
  }
  const last = segments.at(-1)!;
  if (!(last in cursor)) return false;
  delete cursor[last];
  return true;
}

const NUMBER_PATHS = new Set([
  "daemon.port",
  "relay.heartbeatSec",
  "relay.reconnectBackoffMs",
  "notification.retentionDays",
  "lightRules.evaluator.timeoutMs",
  "lightRules.evaluator.retries",
  "image.maxBytes",
]);
const BOOLEAN_PATHS = new Set([
  "daemon.detach",
  "relay.enabled",
  "lightRules.enabled",
  "autoUpdate.enabled",
]);
const ARRAY_PATHS = new Set(["notification.ignoredApps"]);

/** 把命令行字符串值按目标字段类型强转。 */
export function coerceConfigValue(path: string, raw: string): unknown {
  if (NUMBER_PATHS.has(path)) {
    if (path === "notification.retentionDays" && (raw === "null" || raw === "")) {
      return null;
    }
    const n = Number(raw);
    if (!Number.isFinite(n)) {
      throw new YoooclawError(
        "YOOOCLAW_INVALID_ARGUMENT",
        `${path} 需要数字，收到：${raw}`,
      );
    }
    return n;
  }
  if (BOOLEAN_PATHS.has(path)) {
    if (raw === "true") return true;
    if (raw === "false") return false;
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      `${path} 需要 true/false，收到：${raw}`,
    );
  }
  if (ARRAY_PATHS.has(path)) {
    return raw.split(",").map((s) => s.trim()).filter(Boolean);
  }
  return raw;
}

/** 深拷贝并遮罩敏感字段，供 `config show` 用。inline: 引用整体遮罩。 */
export function maskConfig(config: YoooclawConfig): YoooclawConfig {
  const clone = structuredClone(config);
  for (const path of SECRET_REF_PATHS) {
    const value = getByPath(clone, path);
    if (typeof value === "string" && value.startsWith("inline:")) {
      setByPath(clone as unknown as PlainObject, path, "inline:****");
    }
  }
  return clone;
}

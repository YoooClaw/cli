import { randomBytes } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname } from "node:path";
import { readCredentials } from "./auth/credentials.js";
import { resolveStateFile } from "./host.js";

// 容错解析：先按换行切分，再按 KEY= 边界切分，恢复历史上把多条记录粘在同一行的 .env。
// lookbehind 限定前一个字符不是标识符字符，避免在 key 内部错误切分。
function parseEnvContent(content: string): Map<string, string> {
  const entries = new Map<string, string>();
  const tokens = content
    .split(/\r?\n/)
    .flatMap((line) => line.split(/(?<![A-Z0-9_])(?=[A-Z_][A-Z0-9_]*=)/));
  for (const token of tokens) {
    const trimmed = token.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const eq = trimmed.indexOf("=");
    if (eq < 1) continue;
    const key = trimmed.slice(0, eq).trim();
    if (!/^[A-Z_][A-Z0-9_]*$/.test(key)) continue;
    entries.set(key, trimmed.slice(eq + 1).trim());
  }
  return entries;
}

function writeDotEnv(key: string, value: string): void {
  const path = resolveStateFile(".env");
  mkdirSync(dirname(path), { recursive: true });
  const existing = existsSync(path) ? readFileSync(path, "utf-8") : "";
  const entries = parseEnvContent(existing);
  entries.set(key, value);
  const output =
    Array.from(entries, ([k, v]) => `${k}=${v}`).join("\n") + "\n";
  const tmpPath = `${path}.tmp.${process.pid}.${randomBytes(4).toString("hex")}`;
  try {
    // JvsClaw may leave an existing .env read-only while its state directory
    // remains writable. Replacing a sibling temp file avoids reopening it.
    writeFileSync(tmpPath, output, { encoding: "utf-8", mode: 0o600 });
    renameSync(tmpPath, path);
  } catch (error) {
    try {
      rmSync(tmpPath, { force: true });
    } catch {
      // ignore tmp cleanup failure
    }
    throw error;
  }
}

export type EnvName = "development" | "test" | "production";

const DEFAULT_ENV_HOSTS: Record<EnvName, string> = {
  development: "openclaw-service-dev.yoooclaw.com",
  test: "openclaw-service-test.yoooclaw.com",
  production: "openclaw-service.yoooclaw.com",
};

interface EnvUrls {
  lightApiUrl: string;
  appNameMapUrl: string;
  relayTunnelUrl: string;
  modelProxyLongRecordingSubmitTaskUrl: string;
  modelProxyLongRecordingQueryTaskResultBaseUrl: string;
  accountFileDeleteUrl: string;
  gatewayConnectStatusUrl: string;
  /** jvsclaw 实例就绪/密文换 apiKey 接口（复用插件现有接口 host）。 */
  clawManagerInstanceReadyUrl: string;
}

function normalizeHost(host: string | undefined): string | undefined {
  const trimmed = host?.trim();
  if (!trimmed) return undefined;
  return trimmed
    .replace(/^https?:\/\//, "")
    .replace(/^wss?:\/\//, "")
    .replace(/\/+$/, "");
}

function getEnvHost(env: EnvName): string {
  let host: string | undefined;
  switch (env) {
    case "development":
      host = process.env.OPENCLAW_HOST_DEVELOPMENT;
      break;
    case "test":
      host = process.env.OPENCLAW_HOST_TEST;
      break;
    case "production":
      host = process.env.OPENCLAW_HOST_PRODUCTION;
      break;
  }
  return normalizeHost(host) ?? DEFAULT_ENV_HOSTS[env];
}

function buildEnvUrls(host: string): EnvUrls {
  const https = `https://${host}`;
  const wss = `wss://${host}`;
  return {
    lightApiUrl: `${https}/api/message/tob/sendMessage`,
    relayTunnelUrl: `${wss}/message/messages/ws/plugin`,
    appNameMapUrl: `${https}/api/application-config/app-package/config-all`,
    modelProxyLongRecordingSubmitTaskUrl: `${https}/api/model-proxy/long-recording/submit-task`,
    modelProxyLongRecordingQueryTaskResultBaseUrl: `${https}/api/model-proxy/long-recording/query-task-result`,
    accountFileDeleteUrl: `${https}/api/account/file/delete`,
    gatewayConnectStatusUrl: `${https}/api/message/messageBridge/plugin/gateway-connect-status`,
    clawManagerInstanceReadyUrl: `${https}/claw-manager/internal/claw-manager/instance/ready`,
  };
}

const VALID_ENVS: ReadonlySet<string> = new Set<string>([
  "development",
  "test",
  "production",
]);

function readDotEnv(): Record<string, string> {
  const path = resolveStateFile(".env");
  if (!existsSync(path)) return {};
  return Object.fromEntries(parseEnvContent(readFileSync(path, "utf-8")));
}

/** 读取持久化到 .env 的环境名称；仅反映本地配置，不考虑进程环境变量。 */
export function readPersistedEnvName(): EnvName | undefined {
  const fromDotEnv = readDotEnv()["PHONE_NOTIFICATIONS_ENV"]?.trim();
  if (fromDotEnv && VALID_ENVS.has(fromDotEnv)) return fromDotEnv as EnvName;
  return undefined;
}

/** 读取当前环境名称。优先级：PHONE_NOTIFICATIONS_ENV 环境变量 > .env > credentials.json > 默认 production */
export function loadEnvName(): EnvName {
  const fromEnvVar = process.env.PHONE_NOTIFICATIONS_ENV?.trim();
  if (fromEnvVar && VALID_ENVS.has(fromEnvVar)) return fromEnvVar as EnvName;

  const fromDotEnv = readPersistedEnvName();
  if (fromDotEnv) return fromDotEnv;

  const { env } = readCredentials();
  if (env && VALID_ENVS.has(env)) return env as EnvName;

  return "production";
}

/** 持久化当前环境到 .env */
export function saveEnvName(env: EnvName): void {
  if (!VALID_ENVS.has(env)) {
    throw new Error(
      `无效的环境名称: ${env}，可选值: ${[...VALID_ENVS].join(", ")}`,
    );
  }
  writeDotEnv("PHONE_NOTIFICATIONS_ENV", env);
}

/** 获取当前环境的 URL 配置 */
export function getEnvUrls(env?: EnvName): EnvUrls {
  return buildEnvUrls(getEnvHost(env ?? loadEnvName()));
}

/** 获取所有可用环境名称 */
export function getAvailableEnvs(): EnvName[] {
  return ["development", "test", "production"];
}

/**
 * 宿主检测。
 *
 * - detectHostKind()：旧的路径/状态目录家族检测（openclaw | qclaw），行为与 host.ts 完全一致。
 * - resolveClawKind()：新的渠道 Profile 选择（openclaw | arkclaw | jvsclaw），
 *   优先级 PHONE_NOTIFICATIONS_CLAW_PROFILE 环境变量 > 持久化 profile >
 *   jvsclaw 直解 tgz 痕迹 > openclaw。
 */
import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { ClawKind, HostKind } from "./types.js";
import {
  DEFAULT_JVSCLAW_STATE_DIR,
  loadQClawMeta,
  hasOpenClawMarkers,
  resolveConfigPathFromEnv,
  resolvePersistedProfileStateDirs,
  resolvePluginConfigPathOverrideFromEnv,
  resolveStateDirFromEnv,
  trimToUndefined,
} from "./paths.js";

function detectHostKindFromPath(value: string | undefined): HostKind | undefined {
  if (!value) {
    return undefined;
  }
  const normalized = value.replace(/\\/g, "/").toLowerCase();
  if (
    normalized.endsWith("/.qclaw") ||
    normalized.includes("/.qclaw/") ||
    normalized.endsWith("/.qclow") ||
    normalized.includes("/.qclow/")
  ) {
    return "qclaw";
  }
  if (normalized.endsWith("/.openclaw") || normalized.includes("/.openclaw/")) {
    return "openclaw";
  }
  return undefined;
}

export function detectHostKind(): HostKind {
  if (
    trimToUndefined(process.env.QCLAW_STATE_DIR) ||
    trimToUndefined(process.env.QCLAW_CONFIG_PATH) ||
    trimToUndefined(process.env.QCLAW_HOME)
  ) {
    return "qclaw";
  }

  const pathHintKind =
    detectHostKindFromPath(resolveStateDirFromEnv()) ??
    detectHostKindFromPath(resolveConfigPathFromEnv());
  if (pathHintKind) {
    return pathHintKind;
  }

  if (hasOpenClawMarkers()) {
    return "openclaw";
  }

  if (loadQClawMeta()) {
    return "qclaw";
  }

  return "openclaw";
}

const VALID_CLAW_KINDS: ReadonlySet<string> = new Set<ClawKind>([
  "openclaw",
  "arkclaw",
  "jvsclaw",
]);
const PLUGIN_ID = "phone-notifications";

/** 读取持久化到可写 stateDir/.env 的 PHONE_NOTIFICATIONS_CLAW_PROFILE。 */
function readPersistedClawProfile(): string | undefined {
  for (const stateDir of resolvePersistedProfileStateDirs()) {
    let content: string;
    try {
      content = readFileSync(join(stateDir, ".env"), "utf-8");
    } catch {
      continue;
    }
    for (const line of content.split("\n")) {
      const eq = line.indexOf("=");
      if (eq < 1) continue;
      if (line.slice(0, eq).trim() === "PHONE_NOTIFICATIONS_CLAW_PROFILE") {
        return line.slice(eq + 1).trim();
      }
    }
  }
  return undefined;
}

function hasJvsclawEnvHint(): boolean {
  return Boolean(
    trimToUndefined(process.env.JVSCLAW_STATE_DIR) ||
      trimToUndefined(process.env.JVSCLAW_CONFIG_PATH) ||
      trimToUndefined(process.env.JVSCLAW_HOME),
  );
}

function readPluginLinkSecret(configPath: string | undefined): string | undefined {
  if (!configPath) return undefined;

  let config: unknown;
  try {
    config = JSON.parse(readFileSync(configPath, "utf-8"));
  } catch {
    return undefined;
  }

  const entries = readObject(readObject(config, "plugins"), "entries");
  const pluginEntry = readObject(entries, PLUGIN_ID);
  const pluginConfig = readObject(pluginEntry, "config");
  const linkSecret = pluginConfig?.link_secret;
  return typeof linkSecret === "string"
    ? linkSecret.trim() || undefined
    : undefined;
}

function hasJvsclawLinkSecret(): boolean {
  const paths = [
    resolvePluginConfigPathOverrideFromEnv(),
    resolveConfigPathFromEnv(),
    join(DEFAULT_JVSCLAW_STATE_DIR, "openclaw.json"),
  ].filter((value, index, arr): value is string => Boolean(value) && arr.indexOf(value) === index);

  return paths.some((configPath) => Boolean(readPluginLinkSecret(configPath)));
}

function readObject(
  value: unknown,
  key: string,
): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object") {
    return undefined;
  }
  const nested = (value as Record<string, unknown>)[key];
  return nested && typeof nested === "object"
    ? (nested as Record<string, unknown>)
    : undefined;
}

/**
 * 选择渠道 Profile。
 * 优先级：PHONE_NOTIFICATIONS_CLAW_PROFILE 环境变量 > 持久化的 .env（安装脚本 --channel）
 * > jvsclaw 直解 tgz 的运行期/配置痕迹 > 默认 openclaw。
 * 安装脚本带 --channel 时写入 .env，运行期零歧义；jvsclaw 线上直解 tgz 时没有这一步，
 * 需要从 JVSCLAW_* env 或宿主注入的 link_secret 识别。
 */
export function resolveClawKind(): ClawKind {
  const fromEnv = trimToUndefined(process.env.PHONE_NOTIFICATIONS_CLAW_PROFILE)?.toLowerCase();
  if (fromEnv && VALID_CLAW_KINDS.has(fromEnv)) {
    return fromEnv as ClawKind;
  }

  const persisted = readPersistedClawProfile()?.toLowerCase();
  if (persisted && VALID_CLAW_KINDS.has(persisted)) {
    return persisted as ClawKind;
  }

  if (hasJvsclawEnvHint() || hasJvsclawLinkSecret()) {
    return "jvsclaw";
  }

  // 自动检测兜底：qclaw 与 openclaw 同属 apiKey 家族，统一归为 openclaw。
  return "openclaw";
}

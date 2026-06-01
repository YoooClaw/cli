/**
 * auth service —— set-api-key / status / token-rotate / check。
 */
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { readStdin } from "../prompt.js";
import { loadConfig, configExists } from "../config/store.js";
import {
  addApiKey,
  maskSecret,
  removeApiKey,
  resolveApiKey,
  resolveApiKeyEntries,
  setDefaultApiKey,
  setApiKey,
  resolveGatewayToken,
  writeGatewayToken,
  generateToken,
} from "../credentials/store.js";
import { daemonState } from "../daemon/lock.js";
import { DaemonClient, assertDaemonRunning } from "../daemon/client.js";

export async function authSetApiKey(
  ctx: CliContext,
  args: unknown[],
  opts: { keychain?: boolean },
): Promise<unknown> {
  let [key] = args as [string];
  if (key === "-") {
    key = (await readStdin()).trim();
  }
  if (!key) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "api-key 不能为空");
  }
  const result = setApiKey(key, Boolean(opts.keychain));
  return {
    ok: true,
    source: result.source,
    location: result.location,
    label: result.label,
    masked: maskSecret(result.value),
    hint: "插件 / CLI / daemon 共用同一份；daemon 在跑时会经文件 watch 热生效",
  };
}

interface ApiKeyCommandOpts {
  label?: string;
  default?: boolean;
  force?: boolean;
}

export async function authAddApiKey(
  _ctx: CliContext,
  args: unknown[],
  opts: ApiKeyCommandOpts,
): Promise<unknown> {
  let [key] = args as [string];
  if (key === "-") {
    key = (await readStdin()).trim();
  }
  if (!key) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "api-key 不能为空");
  }
  if (!opts.label) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--label 必填");
  }
  const before = resolveApiKeyEntries();
  const result = addApiKey(key, opts.label, {
    makeDefault: Boolean(opts.default),
    force: Boolean(opts.force),
  });
  const after = resolveApiKeyEntries();
  return {
    ok: true,
    mode: after.mode,
    label: opts.label,
    default: after.defaultEntry?.label === opts.label,
    source: result.source,
    location: result.location,
    masked: maskSecret(key),
    shadowedKeychainPresent: after.shadowedKeychainPresent,
    migratedLegacyApiKey: before.mode === "legacy-file-single",
    hint: "daemon 在跑时会经文件 watch 热生效；watch 不可靠时执行 yc daemon reload",
  };
}

function entryToItem(entry: ReturnType<typeof resolveApiKeyEntries>["entries"][number]) {
  return {
    label: entry.label,
    default: entry.default,
    source: entry.source,
    masked: maskSecret(entry.key),
  };
}

export function authListApiKeys(): unknown {
  const set = resolveApiKeyEntries();
  return {
    ok: true,
    mode: set.mode,
    defaultLabel: set.defaultEntry?.label ?? null,
    location: set.location,
    legacyApiKeyPresent: set.legacyApiKeyPresent,
    shadowedKeychainPresent: set.shadowedKeychainPresent,
    warnings: set.warnings,
    items: set.entries.map(entryToItem),
  };
}

export function authRemoveApiKey(
  _ctx: CliContext,
  args: unknown[],
): unknown {
  const [label] = args as [string];
  if (!label) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "label 不能为空");
  }
  const outcome = removeApiKey(label);
  const set = resolveApiKeyEntries();
  return {
    ok: true,
    removed: outcome.removed,
    mode: set.mode,
    defaultLabel: set.defaultEntry?.label ?? null,
    newDefault: outcome.newDefault ?? null,
    remaining: set.entries.length,
  };
}

export function authSetDefaultApiKey(
  _ctx: CliContext,
  args: unknown[],
): unknown {
  const [label] = args as [string];
  if (!label) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "label 不能为空");
  }
  const result = setDefaultApiKey(label);
  const set = resolveApiKeyEntries();
  return {
    ok: true,
    mode: set.mode,
    defaultLabel: set.defaultEntry?.label ?? null,
    source: result.source,
    location: result.location,
  };
}

export function authStatus(ctx: CliContext): unknown {
  const apiKey = resolveApiKey();
  const apiKeys = resolveApiKeyEntries();
  const config = configExists(ctx.paths) ? loadConfig(ctx.paths) : undefined;
  const token = config ? resolveGatewayToken(config) : undefined;
  const state = daemonState(ctx.paths);
  return {
    ok: true,
    profile: ctx.profile,
    mode: apiKeys.mode,
    defaultLabel: apiKeys.defaultEntry?.label ?? null,
    apiKeys: apiKeys.entries.map(entryToItem),
    legacyApiKeyPresent: apiKeys.legacyApiKeyPresent,
    shadowedKeychainPresent: apiKeys.shadowedKeychainPresent,
    warnings: apiKeys.warnings,
    apiKey: {
      present: Boolean(apiKey.value),
      source: apiKey.source,
      location: apiKey.location,
      label: apiKey.label,
      masked: maskSecret(apiKey.value),
    },
    gatewayToken: {
      present: Boolean(token?.value),
      source: token?.source,
      location: token?.location,
      masked: maskSecret(token?.value),
    },
    daemon: {
      running: state.running,
      pid: state.lock?.pid,
      stale: state.stale,
    },
  };
}

export function authTokenRotate(
  ctx: CliContext,
  _args: unknown[],
  opts: { length?: string },
): unknown {
  if (!configExists(ctx.paths)) {
    throw new YoooclawError("YOOOCLAW_CONFIG_INVALID", `profile \`${ctx.profile}\` 尚未初始化`, {
      hint: "先运行 yoooclaw config init",
    });
  }
  const config = loadConfig(ctx.paths);
  const bytes = opts.length ? Number(opts.length) : 32;
  if (!Number.isInteger(bytes) || bytes < 16) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--length 至少 16 字节");
  }
  const token = generateToken(bytes);
  const written = writeGatewayToken(config, token);
  const state = daemonState(ctx.paths);
  // daemon 在跑时尝试热重载（best-effort，失败不影响 token 已写入）
  let hotReload: string;
  if (state.running) {
    hotReload = "daemon 在运行：请执行 yoooclaw daemon restart 使新 token 生效";
  } else {
    hotReload = "daemon 未运行：下次启动即用新 token";
  }
  return {
    ok: true,
    token,
    source: written.source,
    location: written.location,
    hint: hotReload,
  };
}

export async function authCheck(ctx: CliContext): Promise<unknown> {
  assertDaemonRunning(ctx.paths);
  const client = new DaemonClient(ctx.paths);
  const res = await client.get("/daemon/status");
  return {
    ok: res.status >= 200 && res.status < 300,
    profile: ctx.profile,
    daemonReachable: true,
    status: res.status,
    daemon: res.body,
  };
}

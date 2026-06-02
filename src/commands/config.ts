/**
 * config service —— init / show / set / unset。
 */
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { readJsonFile, ensureDir } from "../fs-utils.js";
import { readStdin, ask, confirm, isInteractive } from "../prompt.js";
import {
  configExists,
  loadConfig,
  saveConfig,
  coerceConfigValue,
  deepMerge,
  getByPath,
  setByPath,
  unsetByPath,
  maskConfig,
} from "../config/store.js";
import { defaultConfig, defaultEvaluator, type YoooclawConfig } from "../config/schema.js";
import { writeGatewayToken, generateToken, resolveApiKey, setApiKey } from "../credentials/store.js";
import { daemonStart } from "./daemon.js";

interface InitOpts {
  nonInteractive?: boolean;
  fromFile?: string;
  force?: boolean;
  start?: boolean; // commander：--no-start → false，跳过自动拉起 daemon
}

/** 手机端配置摘要 + 关键路径，作为 init 的返回结果。 */
function phoneSummary(
  ctx: CliContext,
  config: YoooclawConfig,
  token: string,
  daemon: DaemonStartSummary,
) {
  const apiKey = resolveApiKey();
  return {
    ok: true,
    profile: ctx.profile,
    daemon: {
      // bind/port 由 daemon 自管：默认绑回环，端口被占自动 +1，无需用户配置。
      bind: config.daemon.bind,
      port: config.daemon.port,
      localUrl: `http://${config.daemon.bind}:${config.daemon.port}`,
      started: daemon.started,
      pid: daemon.pid,
    },
    relay: {
      url: config.relay.url,
      enabled: config.relay.enabled,
      apiKeyPresent: Boolean(apiKey.value),
    },
    gatewayToken: token,
    configPath: ctx.paths.config,
    hint: initHint(daemon, Boolean(apiKey.value)),
  };
}

/** 按 daemon 是否拉起、是否已有 api-key 给出下一步提示。 */
function initHint(daemon: DaemonStartSummary, apiKeyPresent: boolean): string {
  if (!daemon.started) {
    const reason = daemon.error ? `（自动启动失败：${daemon.error}）` : "";
    return apiKeyPresent
      ? `配置已就绪${reason}：运行 yc daemon start 启动 daemon，连上 Relay 后手机 App 绑定同一账号即可收发`
      : `配置已就绪${reason}：运行 yc auth set-api-key <ock_…> 设置 Relay api-key，再 yc daemon start 启动 daemon`;
  }
  return apiKeyPresent
    ? "已就绪：daemon 已启动并用 api-key 连上 Relay；手机 App 绑定同一账号即可收发"
    : "daemon 已启动，但 Relay 尚未设置 api-key：运行 yc auth set-api-key <ock_…> 后 yc daemon restart 即可连上 Relay";
}

export async function configInit(
  ctx: CliContext,
  _args: unknown[],
  opts: InitOpts,
): Promise<unknown> {
  if (configExists(ctx.paths) && !opts.force) {
    throw new YoooclawError(
      "YOOOCLAW_ALREADY_EXISTS",
      `profile \`${ctx.profile}\` 已初始化`,
      { hint: "加 --force 覆盖，或换一个 --profile", checkedPaths: [ctx.paths.config] },
    );
  }

  ensureDir(ctx.paths.dir);
  let config = defaultConfig(ctx.paths.credentials);

  if (opts.nonInteractive || !isInteractive()) {
    if (!opts.fromFile) {
      throw new YoooclawError(
        "YOOOCLAW_NOT_INTERACTIVE",
        "非交互模式必须提供 --from-file <config.json>（- 为 stdin）",
      );
    }
    const raw = opts.fromFile === "-" ? await readStdin() : undefined;
    const imported = raw
      ? (JSON.parse(raw) as Partial<YoooclawConfig>)
      : readJsonFile<Partial<YoooclawConfig>>(opts.fromFile);
    if (!imported) {
      throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", `无法读取配置文件：${opts.fromFile}`);
    }
    config = deepMerge(config, imported);
    config.version = defaultConfig(ctx.paths.credentials).version;
  } else {
    // daemon 监听地址/端口不再让用户配置：默认绑回环，端口被占时 daemon 启动会自动 +1。
    // relay 复用插件那套托管服务（config.relay.url 已是生产地址），用户只需提供 api-key。
    const existing = resolveApiKey();
    if (existing.value) {
      console.error(`已检测到 Relay api-key（来源 ${existing.source}），跳过设置`);
    } else {
      const key = (await ask("Relay api-key（ock_… ，留空可稍后 yc auth set-api-key 设置）")).trim();
      if (key) setApiKey(key, false);
    }
    const enableEval = await confirm("启用灯效 webhook 评估器？", false);
    if (enableEval) {
      const evaluator = defaultEvaluator(ctx.paths.credentials);
      evaluator.webhookUrl = await ask("评估器 webhook URL");
      config.lightRules.evaluator = evaluator;
    }
  }

  // 生成并落 gateway token（按 config.auth.tokenRef，默认 per-profile credentials.json）
  const token = generateToken();
  saveConfig(ctx.paths, config);
  writeGatewayToken(config, token);

  // init 即用：直接把 daemon 拉起来，省去用户再单独跑一次 yc daemon start。
  // --no-start 可跳过（例如只想生成配置、稍后手动启动）。
  let daemon: DaemonStartSummary = { started: false };
  if (opts.start !== false) {
    daemon = await startDaemonForInit(ctx);
  }

  return phoneSummary(ctx, config, token, daemon);
}

interface DaemonStartSummary {
  started: boolean;
  pid?: number;
  alreadyRunning?: boolean;
  error?: string;
}

/** init 收尾时拉起 daemon；失败不阻断 init（配置已落盘），把结果带回摘要由用户决定下一步。 */
async function startDaemonForInit(ctx: CliContext): Promise<DaemonStartSummary> {
  try {
    const res = (await daemonStart(ctx, [], {})) as { pid?: number };
    return { started: true, pid: res.pid };
  } catch (err) {
    if (err instanceof YoooclawError && err.code === "YOOOCLAW_DAEMON_ALREADY_RUNNING") {
      return { started: true, alreadyRunning: true, pid: err.details?.pid as number | undefined };
    }
    return { started: false, error: err instanceof Error ? err.message : String(err) };
  }
}

interface ShowOpts {
  showSecrets?: boolean;
}

export async function configShow(
  ctx: CliContext,
  _args: unknown[],
  opts: ShowOpts,
): Promise<unknown> {
  if (!configExists(ctx.paths)) {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `profile \`${ctx.profile}\` 尚未初始化`,
      { hint: "先运行 yoooclaw config init", checkedPaths: [ctx.paths.config] },
    );
  }
  const config = loadConfig(ctx.paths);
  if (opts.showSecrets) {
    if (!isInteractive()) {
      throw new YoooclawError(
        "YOOOCLAW_NOT_INTERACTIVE",
        "--show-secrets 需要在 TTY 中运行",
      );
    }
    if (!(await confirm("确认明文输出敏感字段？", false))) {
      throw new YoooclawError("YOOOCLAW_CONFIRMATION_REQUIRED", "已取消");
    }
    return config;
  }
  return maskConfig(config);
}

export function configSet(
  ctx: CliContext,
  args: unknown[],
  _opts: unknown,
): unknown {
  const [key, value] = args as [string, string];
  if (key === "version") {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "version 字段不可修改");
  }
  const config = loadConfig(ctx.paths) as unknown as Record<string, unknown>;
  setByPath(config, key, coerceConfigValue(key, value));
  saveConfig(ctx.paths, config as unknown as YoooclawConfig);
  return { ok: true, key, value: getByPath(config, key) };
}

export function configUnset(
  ctx: CliContext,
  args: unknown[],
  _opts: unknown,
): unknown {
  const [key] = args as [string];
  const config = loadConfig(ctx.paths) as unknown as Record<string, unknown>;
  const removed = unsetByPath(config, key);
  if (removed) saveConfig(ctx.paths, config as unknown as YoooclawConfig);
  return { ok: true, key, removed };
}

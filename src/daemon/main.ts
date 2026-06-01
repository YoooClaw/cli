/**
 * daemon 主循环（前台运行体）。`daemon start` 默认 fork 一个子进程跑 `daemon run-foreground`，
 * 该命令最终调用这里的 runDaemonForeground。
 *
 * 职责：单实例锁 → 装配 StandaloneRuntime + 存储 + 通知/图片/灯效规则注册 → 起 HTTP server →
 * 起 Relay 隧道 → 写 lock → 捕获信号优雅退出。
 *
 * 关于 Relay：复用 phone-notifications 插件那套托管 Relay 服务（RelayClient + TunnelProxy，见
 * daemon/relay.ts）。daemon 用 account 级 api-key 连上 Relay，手机端经 Relay 转发进来的请求被反代到
 * 本 daemon 的 HTTP server。未设 api-key 时跳过隧道，仍可走直连 HTTP（cloudflared / tailscale serve）。
 */
import { watchFile, unwatchFile } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse, type Server } from "node:http";
import type { CliContext } from "../context.js";
import { loadConfig } from "../config/store.js";
import { sharedCredentialsPath } from "../paths.js";
import { resolveGatewayToken } from "../credentials/store.js";
import { resolveApiKeyEntries, type CredentialSet } from "../credentials/store.js";
import { daemonState, writeLock, removeLock } from "./lock.js";
import { DaemonLogger } from "./logger.js";
import { StandaloneRuntime } from "./runtime.js";
import { YoooclawError } from "../errors.js";
import { ensureDir } from "../fs-utils.js";
import { ingestImage, type ImageSyncPayload } from "../image/channel.js";
import { listMonitors, createMonitor, deleteMonitor, setMonitorEnabled } from "../monitor/store.js";
import { CLI_VERSION } from "../version.js";
import {
  NotificationStorage,
  registerNotificationInterfaces,
  RecordingStorage,
  registerRecordingInterfaces,
  LightRuleRegistry,
  registerLightRulesGateway,
  RELAY_INTERNAL_HTTP_HEADER,
  runWithIngestContext,
  type IngestContext,
  type PluginConfig,
} from "../shared.js";
import { performLightSend } from "./light-send.js";
import { RecordingEventLog } from "./recording-events.js";
import { overrideRecordingSync } from "./recording-bridge.js";
import { registerDaemonGatewayCompatMethods } from "./gateway-compat.js";
import { RELAY_INTERNAL_CLIENT_LABEL_HEADER } from "./relay-dispatcher.js";
import { TunnelSupervisor } from "./tunnel-supervisor.js";

export const PROTOCOL_VERSION = 1;
export const CAPABILITIES = ["notifications", "recordings", "images", "lightrules", "multi-apikey"];

export interface DaemonStartOpts {
  bind?: string;
  port?: number;
  logLevel?: string;
}

interface DaemonState {
  startedAt: string;
  lastIngestAt: string | null;
  ingestCount: number;
}

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return Buffer.concat(chunks).toString("utf-8");
}

function sendJson(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

export async function runDaemonForeground(
  ctx: CliContext,
  opts: DaemonStartOpts,
): Promise<void> {
  const state = daemonState(ctx.paths);
  if (state.running) {
    throw new YoooclawError(
      "YOOOCLAW_DAEMON_ALREADY_RUNNING",
      `daemon 已在运行（pid ${state.lock?.pid}）`,
    );
  }

  const config = loadConfig(ctx.paths);
  const bind = opts.bind ?? config.daemon.bind;
  // 起始端口；listen 时若被占用会自动 +1（见下方 listen 循环），port 最终为实际绑定端口。
  let port = opts.port ?? config.daemon.port;
  const logLevel = (opts.logLevel ?? config.daemon.logLevel) as never;
  const token = resolveGatewayToken(config).value;
  let credentialSet: CredentialSet = resolveApiKeyEntries();

  // 安全：绑公网必须有 token
  const loopback = bind === "127.0.0.1" || bind === "::1" || bind === "localhost";
  if (!loopback && !token) {
    throw new YoooclawError(
      "YOOOCLAW_UNAUTHORIZED",
      `绑定 ${bind} 需要先设置 gateway token`,
      { hint: "运行 yoooclaw auth token-rotate 或改回 127.0.0.1" },
    );
  }

  ensureDir(ctx.paths.dir);
  const logger = new DaemonLogger(ctx.paths.daemonLog, logLevel, false);
  const runtimeState: DaemonState = {
    startedAt: new Date().toISOString(),
    lastIngestAt: null,
    ingestCount: 0,
  };

  const pluginConfig: PluginConfig = {
    retentionDays: config.notification.retentionDays ?? undefined,
    ignoredApps: config.notification.ignoredApps,
  };

  const runtime = new StandaloneRuntime(logger, pluginConfig as Record<string, unknown>, ctx.paths.dir);

  // 通知存储
  const storage = new NotificationStorage(ctx.paths.notifications, pluginConfig, logger);
  await storage.init();

  // 录音存储（接收手机端 recordings.sync / POST /recordings）
  const recordingStorage = new RecordingStorage(ctx.paths.recordings, logger);
  await recordingStorage.init();

  // 录音状态事件流：日志 + JSONL（CLI / Agent 通过 `yc recording events` 消费）+ 反向推手机
  const recordingEventLog = new RecordingEventLog(ctx.paths.recordings);
  // pushRecordingStatus 在 relayHandle 启动后才有值；用 mutable 闭包变量延后绑定
  let pushRecordingStatus: ((event: import("../shared.js").RecordingStatusEvent) => void) | undefined;
  const notifyRecordingStatus = (event: import("../shared.js").RecordingStatusEvent) => {
    logger.info(
      `[recording-status] ${event.recordingId} → ${event.transfer_status}${
        event.error ? ` err=${event.error}` : ""
      }`,
    );
    try {
      recordingEventLog.append(event);
    } catch (err) {
      logger.warn(`[recording-status] 事件落盘失败: ${(err as Error).message}`);
    }
    // 反向推送到手机端（客户端合同：recording.status event；recordings.status RPC 是兜底）
    try {
      pushRecordingStatus?.(event);
    } catch (err) {
      logger.warn(`[recording-status] event push 失败: ${(err as Error).message}`);
    }
  };

  // 灯效规则注册（rules 落 <profile>/tasks/）
  const lightRuleRegistry = new LightRuleRegistry({ workspaceDir: ctx.paths.dir });

  const ignored = new Set(config.notification.ignoredApps ?? []);
  registerNotificationInterfaces({
    // 结构兼容 OpenClawPluginApi 的子集
    api: runtime as never,
    logger,
    getStorage: () => storage,
    filterNotifications: (items) => items.filter((n) => !ignored.has(n.app)),
    registerGatewayMethod: (m, h) => runtime.registerGatewayMethod(m, h as never),
    onAfterIngest: (inserted) => {
      runtimeState.lastIngestAt = new Date().toISOString();
      runtimeState.ingestCount += inserted.length;
    },
  });
  registerRecordingInterfaces({
    api: runtime as never,
    logger,
    asrDataDir: ctx.paths.recordings,
    getRecordingStorage: () => recordingStorage,
    notifyRecordingStatus,
    registerGatewayMethod: (m, h) => runtime.registerGatewayMethod(m, h as never),
    shouldBroadcastStatusOnHttp: () => true,
    tunnelService: null,
  });
  // 覆盖 sync：注入本地 asr-config.json + account 级 ock- key fallback
  overrideRecordingSync({
    runtime,
    storage: recordingStorage,
    logger,
    fallbackApiKey: () => credentialSet.defaultEntry?.key,
    asrConfigDir: ctx.paths.recordings,
    notifyStatus: notifyRecordingStatus,
  });
  registerLightRulesGateway(runtime as never, lightRuleRegistry, logger);

  const server = createServer((req, res) => {
    void handleRequest(req, res).catch((err) => {
      logger.error(`请求处理异常：${(err as Error).message}`);
      if (!res.headersSent) sendJson(res, 500, { ok: false, error: { code: "INTERNAL_ERROR", message: (err as Error).message } });
    });
  });

  function headerValue(req: IncomingMessage, name: string): string | undefined {
    const value = req.headers[name.toLowerCase()];
    return Array.isArray(value) ? value[0] : value;
  }

  function bearerValue(req: IncomingMessage): string | undefined {
    const header = headerValue(req, "authorization")?.trim();
    if (!header?.toLowerCase().startsWith("bearer ")) return undefined;
    return header.slice("Bearer ".length).trim();
  }

  function stripBearer(value: string): string {
    return value.startsWith("Bearer ") ? value.slice("Bearer ".length) : value;
  }

  function labelForApiKey(apiKey: string | undefined): string | undefined {
    if (!apiKey) return undefined;
    const raw = stripBearer(apiKey);
    return credentialSet.entries.find((entry) => stripBearer(entry.key) === raw)?.label;
  }

  function isIngestPath(path: string): boolean {
    return path === "/notifications"
      || path === "/recordings"
      || path === "/images"
      || path === "/gateway/notifications.push"
      || path === "/gateway/recordings.sync"
      || path === "/gateway/images.sync";
  }

  function authContextForRequest(req: IncomingMessage, path: string): { ok: true; context: IngestContext } | { ok: false } {
    const internalRelay = headerValue(req, RELAY_INTERNAL_HTTP_HEADER) === "1";
    const internalLabel = headerValue(req, RELAY_INTERNAL_CLIENT_LABEL_HEADER)?.trim();
    const bearer = bearerValue(req);
    const gatewayTokenOk = token ? bearer === token : true;

    if (internalRelay && internalLabel && gatewayTokenOk) {
      return { ok: true, context: { clientLabel: internalLabel, authKind: "relay-api-key" } };
    }

    if (token && bearer === token) {
      return { ok: true, context: { clientLabel: "local", authKind: "gateway-token" } };
    }

    if (isIngestPath(path)) {
      const label = labelForApiKey(bearer);
      if (label) {
        return { ok: true, context: { clientLabel: label, authKind: "http-api-key" } };
      }
    }

    if (!token) {
      return { ok: true, context: { clientLabel: "local", authKind: "local" } };
    }

    return { ok: false };
  }

  /** 汇总当前 Relay 状态（隧道未启用/未连时给出可执行提示）。 */
  function relayStatus(): {
    mode: "relay" | "standalone-http";
    connected: boolean;
    url: string;
    enabled: boolean;
    reconnectAttempt: number;
    lastDisconnectReason?: string;
    note?: string;
  } {
    if (tunnelSupervisor) {
      const s = tunnelSupervisor.defaultStatus();
      if (s) {
        return {
          mode: "relay",
          connected: s.connected,
          url: config.relay.url,
          enabled: config.relay.enabled,
          reconnectAttempt: s.reconnectAttempt,
          lastDisconnectReason: s.lastDisconnectReason,
          note: s.connected ? undefined : "Relay 重连中",
        };
      }
      return {
        mode: "relay",
        connected: false,
        url: config.relay.url,
        enabled: config.relay.enabled,
        reconnectAttempt: 0,
        note: "Relay 已启用但当前 CredentialSet 没有可用 api-key",
      };
    }
    return {
      mode: "standalone-http",
      connected: false,
      url: config.relay.url,
      enabled: config.relay.enabled,
      reconnectAttempt: 0,
      note: config.relay.enabled
        ? "Relay 已启用但未设置 api-key：运行 yc auth set-api-key <ock_…> 后重启 daemon；当前可走直连 HTTP（cloudflared / tailscale serve）"
        : "Relay 未启用，走直连 HTTP",
    };
  }

  registerDaemonGatewayCompatMethods(runtime, {
    version: CLI_VERSION,
    protocol: PROTOCOL_VERSION,
    capabilities: CAPABILITIES,
    profile: ctx.profile,
    bind,
    getPort: () => port,
    startedAt: runtimeState.startedAt,
    getLastIngestAt: () => runtimeState.lastIngestAt,
    getIngestCount: () => runtimeState.ingestCount,
    getLightRuleCount: () => lightRuleRegistry.list().length,
    getRelayStatus: relayStatus,
  });

  async function handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://localhost");
    const path = url.pathname;
    const method = req.method ?? "GET";

    // /health 公开（手机端协议协商）
    if (path === "/health" && method === "GET") {
      sendJson(res, 200, {
        server: "yoooclaw",
        version: CLI_VERSION,
        protocol: PROTOCOL_VERSION,
        capabilities: CAPABILITIES,
      });
      return;
    }

    const auth = authContextForRequest(req, path);
    if (!auth.ok) {
      sendJson(res, 401, { ok: false, error: { code: "YOOOCLAW_UNAUTHORIZED", message: "token 不一致或缺失" } });
      return;
    }
    const ingestContext = auth.context;

    // ── 控制面 ──
    if (path === "/daemon/status" && method === "GET") {
      const supervisorStatus = tunnelSupervisor?.status();
      sendJson(res, 200, {
        ok: true,
        server: "yoooclaw",
        version: CLI_VERSION,
        pid: process.pid,
        profile: ctx.profile,
        bind,
        port,
        startedAt: runtimeState.startedAt,
        lastIngestAt: runtimeState.lastIngestAt,
        ingestCount: runtimeState.ingestCount,
        lightRules: lightRuleRegistry.list().length,
        relay: relayStatus(),
        tunnels: supervisorStatus?.tunnels ?? [],
        credentialMode: credentialSet.mode,
        defaultLabel: credentialSet.defaultEntry?.label ?? null,
        credentialWarnings: credentialSet.warnings,
        memoryRssBytes: process.memoryUsage().rss,
      });
      return;
    }
    if (path === "/daemon/reload" && method === "POST") {
      const result = await reloadCredentialsAndTunnels("manual");
      sendJson(res, 200, { ok: true, running: true, reloaded: true, ...result });
      return;
    }
    if (path === "/daemon/stop" && method === "POST") {
      sendJson(res, 200, { ok: true, stopping: true });
      logger.info("收到 /daemon/stop，准备优雅退出");
      setTimeout(() => void shutdown("stop-endpoint"), 50);
      return;
    }
    if (path === "/tunnel/status" && method === "GET") {
      const s = relayStatus();
      const supervisorStatus = tunnelSupervisor?.status();
      const clientLabel = url.searchParams.get("client")?.trim();
      const tunnels = clientLabel && clientLabel !== "all"
        ? (supervisorStatus?.tunnels ?? []).filter((tunnel) => tunnel.label === clientLabel)
        : supervisorStatus?.tunnels ?? [];
      sendJson(res, 200, {
        ok: true,
        mode: s.mode,
        credentialMode: credentialSet.mode,
        defaultLabel: supervisorStatus?.defaultLabel ?? credentialSet.defaultEntry?.label ?? null,
        connected: s.connected,
        relayUrl: config.relay.url,
        enabled: config.relay.enabled,
        reconnectAttempt: s.reconnectAttempt,
        lastDisconnectReason: s.lastDisconnectReason,
        note: s.note,
        tunnels,
      });
      return;
    }
    if (path === "/tunnel/reconnect" && method === "POST") {
      if (!tunnelSupervisor) {
        sendJson(res, 200, { ok: true, mode: relayStatus().mode, reconnected: false, note: relayStatus().note });
        return;
      }
      const body = await parseJson(req, res);
      if (body === undefined) return;
      const clientLabel = typeof (body as { client?: unknown }).client === "string"
        ? (body as { client: string }).client
        : undefined;
      const result = await tunnelSupervisor.reconnect(clientLabel);
      if (result.missing) {
        sendJson(res, 404, { ok: false, error: { code: "YOOOCLAW_TUNNEL_LABEL_NOT_FOUND", message: `隧道不存在：${result.missing}` } });
        return;
      }
      sendJson(res, 200, { ok: true, mode: "relay", reconnected: true, labels: result.reconnected });
      return;
    }
    if (path === "/tunnel/test" && method === "POST") {
      const body = await parseJson(req, res);
      if (body === undefined) return;
      const clientLabel = typeof (body as { client?: unknown }).client === "string"
        ? (body as { client: string }).client.trim()
        : undefined;
      const entry = clientLabel && clientLabel !== "all"
        ? credentialSet.entries.find((candidate) => candidate.label === clientLabel)
        : undefined;
      if (clientLabel && clientLabel !== "all" && !entry) {
        sendJson(res, 404, { ok: false, error: { code: "YOOOCLAW_TUNNEL_LABEL_NOT_FOUND", message: `隧道不存在：${clientLabel}` } });
        return;
      }
      // 回环自检：daemon 自己 POST 一条 echo 通知到本地 /notifications
      const echo = await selfEchoCheck(`http://127.0.0.1:${port}`, {
        token: entry ? undefined : token,
        apiKey: entry?.key,
        clientLabel: entry?.label ?? "local",
      });
      sendJson(res, 200, { ok: echo.ok, mode: relayStatus().mode, clientLabel: entry?.label ?? "local", loopback: echo });
      return;
    }
    if (path === "/light/send" && method === "POST") {
      const body = await parseJson(req, res);
      if (body === undefined) return;
      const outcome = await performLightSend(body as Parameters<typeof performLightSend>[0], {
        logger,
        registry: lightRuleRegistry,
      });
      sendJson(res, outcome.status, outcome.body);
      return;
    }
    if (path === "/images" && method === "POST") {
      const body = await parseJson(req, res);
      if (body === undefined) return;
      try {
        const result = runWithIngestContext(ingestContext, () =>
          ingestImage(ctx.paths, body as ImageSyncPayload, {
            maxBytes: config.image.maxBytes,
            logger,
          }));
        sendJson(res, 200, result);
      } catch (err) {
        sendJson(res, 400, { ok: false, error: { code: "INVALID_PARAMS", message: (err as Error).message } });
      }
      return;
    }
    // monitors CRUD（定义存储；本 build 不做 live cron 触发）
    if (path === "/monitors" && method === "GET") {
      sendJson(res, 200, { ok: true, monitors: listMonitors(ctx.paths) });
      return;
    }
    if (path === "/monitors" && method === "POST") {
      const body = await parseJson(req, res);
      if (body === undefined) return;
      try {
        sendJson(res, 200, { ok: true, monitor: createMonitor(ctx.paths, body as never) });
      } catch (err) {
        sendJson(res, 400, { ok: false, error: { code: "INVALID_PARAMS", message: (err as Error).message } });
      }
      return;
    }
    if (path.startsWith("/monitors/") && method === "DELETE") {
      const name = decodeURIComponent(path.slice("/monitors/".length));
      sendJson(res, 200, { ok: true, deleted: deleteMonitor(ctx.paths, name) });
      return;
    }
    if (path.startsWith("/monitors/") && method === "POST") {
      const [, , name, action] = path.split("/");
      const enabled = action === "enable";
      sendJson(res, 200, { ok: setMonitorEnabled(ctx.paths, decodeURIComponent(name), enabled), name, enabled });
      return;
    }

    // gateway 桥：POST /gateway/<method>
    if (path.startsWith("/gateway/") && method === "POST") {
      const gwMethod = path.slice("/gateway/".length);
      const body = await parseJson(req, res);
      if (body === undefined) return;
      const result = await runtime.callGateway(gwMethod, body, ingestContext);
      sendJson(res, result.ok ? 200 : 400, result);
      return;
    }

    // 注册的 HTTP 路由（如 POST /notifications）
    const route = runtime.httpRoutes.get(path);
    if (route) {
      await runWithIngestContext(ingestContext, () => route.handler(req, res));
      return;
    }

    sendJson(res, 404, { ok: false, error: { code: "YOOOCLAW_NOT_FOUND", message: `未知路径：${path}` } });
  }

  async function parseJson(req: IncomingMessage, res: ServerResponse): Promise<unknown | undefined> {
    try {
      const raw = await readBody(req);
      return raw ? JSON.parse(raw) : {};
    } catch {
      sendJson(res, 400, { ok: false, error: { code: "INVALID_PARAMS", message: "请求体不是合法 JSON" } });
      return undefined;
    }
  }

  let serverRef: Server | null = null;
  let tunnelSupervisor: TunnelSupervisor | null = null;
  let credentialWatchTimer: NodeJS.Timeout | null = null;
  let shuttingDown = false;

  async function reloadCredentialsAndTunnels(reason: string): Promise<{
    mode: CredentialSet["mode"];
    defaultLabel: string | null;
    started: string[];
    stopped: string[];
    restarted: string[];
    unchanged: string[];
    warnings: string[];
  }> {
    credentialSet = resolveApiKeyEntries();
    const result = tunnelSupervisor
      ? await tunnelSupervisor.apply(credentialSet)
      : { started: [], stopped: [], restarted: [], unchanged: [] };
    logger.info(
      `CredentialSet reload(${reason}): mode=${credentialSet.mode}, default=${credentialSet.defaultEntry?.label ?? "-"}, ` +
      `started=${result.started.join(",") || "-"} stopped=${result.stopped.join(",") || "-"} restarted=${result.restarted.join(",") || "-"}`,
    );
    return {
      mode: credentialSet.mode,
      defaultLabel: credentialSet.defaultEntry?.label ?? null,
      warnings: credentialSet.warnings,
      ...result,
    };
  }

  function startCredentialWatcher(): void {
    const file = sharedCredentialsPath();
    watchFile(file, { interval: 500 }, () => {
      if (credentialWatchTimer) clearTimeout(credentialWatchTimer);
      credentialWatchTimer = setTimeout(() => {
        void reloadCredentialsAndTunnels("watch").catch((err) => {
          logger.warn(`CredentialSet watch reload failed: ${(err as Error).message}`);
        });
      }, 200);
    });
  }

  async function shutdown(reason: string): Promise<void> {
    if (shuttingDown) return;
    shuttingDown = true;
    logger.info(`daemon 退出（${reason}）`);
    unwatchFile(sharedCredentialsPath());
    if (credentialWatchTimer) clearTimeout(credentialWatchTimer);
    try {
      await tunnelSupervisor?.stopAll(reason);
    } catch {
      /* ignore */
    }
    try {
      await storage.close();
    } catch {
      /* ignore */
    }
    try {
      await recordingStorage.close();
    } catch {
      /* ignore */
    }
    serverRef?.close();
    removeLock(ctx.paths);
    process.exit(0);
  }

  // listen；遇 EADDRINUSE（端口被其它进程占用）则端口 +1 重试，port 最终为实际绑定端口。
  const listenOnce = (p: number): Promise<void> =>
    new Promise<void>((resolve, reject) => {
      const onError = (err: NodeJS.ErrnoException) => {
        server.removeListener("listening", onListening);
        reject(err);
      };
      const onListening = () => {
        server.removeListener("error", onError);
        resolve();
      };
      server.once("error", onError);
      server.once("listening", onListening);
      server.listen(p, bind);
    });

  const startPort = port;
  for (let attempt = 0; ; attempt += 1) {
    try {
      await listenOnce(port);
      break;
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === "EADDRINUSE" && attempt < 64) {
        const next = port + 1;
        logger.warn(`端口 ${port} 被占用，改试 ${next}`);
        port = next;
        continue;
      }
      throw err;
    }
  }
  if (port !== startPort) {
    logger.info(`起始端口 ${startPort} 被占用，最终绑定到 ${port}`);
  }
  serverRef = server;
  writeLock(ctx.paths, {
    pid: process.pid,
    startedAt: runtimeState.startedAt,
    bind,
    port,
    logLevel: String(logLevel),
  });
  logger.info(`yoooclaw daemon 启动：${bind}:${port}（profile=${ctx.profile}, pid=${process.pid}）`);

  // Relay 隧道：复用插件连接逻辑，反代到本 daemon 自己的 HTTP server。
  if (config.relay.enabled) {
    tunnelSupervisor = new TunnelSupervisor({
      tunnelUrl: config.relay.url,
      runtime,
      httpBaseUrl: `http://127.0.0.1:${port}`,
      httpToken: token,
      heartbeatSec: config.relay.heartbeatSec,
      reconnectBackoffMs: config.relay.reconnectBackoffMs,
      stateDir: ctx.paths.dir,
      logger,
    });
    const result = await tunnelSupervisor.apply(credentialSet);
    pushRecordingStatus = (event) => tunnelSupervisor?.pushEvent("recording.status", event);
    startCredentialWatcher();
    if (credentialSet.entries.length > 0) {
      logger.info(
        `Relay 多隧道已启动：${result.started.join(",") || "-"}（mode=${credentialSet.mode}, default=${credentialSet.defaultEntry?.label ?? "-"}）`,
      );
    } else {
      logger.warn("Relay 已启用但未设置 api-key，跳过隧道连接。运行 yc auth set-api-key <ock_…> 或 yc auth add-api-key <ock_…> --label <label>");
    }
  } else {
    logger.info("Relay 未启用（config.relay.enabled=false），仅走直连 HTTP");
  }

  process.on("SIGTERM", () => void shutdown("SIGTERM"));
  process.on("SIGINT", () => void shutdown("SIGINT"));

  // 前台保持运行（http server 维持 event loop）；返回的 promise 永不 resolve
  await new Promise<void>(() => {});
}

async function selfEchoCheck(
  baseUrl: string,
  opts: { token?: string; apiKey?: string; clientLabel?: string } = {},
): Promise<{ ok: boolean; status?: number; clientLabel?: string; error?: string }> {
  try {
    const authValue = opts.apiKey
      ? opts.apiKey.replace(/^Bearer\s+/i, "")
      : opts.token;
    const res = await fetch(`${baseUrl}/notifications`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(authValue ? { Authorization: `Bearer ${authValue}` } : {}),
      },
      body: JSON.stringify({
        notifications: [
          {
            id: `echo_${opts.clientLabel ?? "local"}_${Date.now()}`,
            app: "yoooclaw.selftest",
            title: "tunnel +test",
            body: "echo",
            timestamp: new Date().toISOString(),
          },
        ],
      }),
    });
    return { ok: res.ok, status: res.status, clientLabel: opts.clientLabel };
  } catch (err) {
    return { ok: false, clientLabel: opts.clientLabel, error: (err as Error).message };
  }
}

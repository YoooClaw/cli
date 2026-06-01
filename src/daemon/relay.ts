/**
 * Relay 隧道接入 —— daemon 模式专用。
 *
 * 与插件的差别（也是这次解耦的核心）：
 *   - **复用** RelayClient：连接、心跳、重连、frame 协议跟插件完全一致 —— 这部分本来就跟 OpenClaw 无关。
 *   - **不复用** TunnelProxy：那玩意把每个 req 反代到 OpenClaw 桌面端的 gateway WS（含 connect.challenge /
 *     pairing），daemon 不实现 OpenClaw 那套协议，反代会 401 卡死。
 *   - 改用 RelayDispatcher：拿到 req 帧后直接同进程交给 runtime.callGateway 处理，再以 res 帧回写。
 *
 * 对手机端 / Relay 服务端完全透明：协议帧（req/res schema、心跳、认证）都不变，只是 daemon 内部「frame
 * 接到后怎么处理」从「反代给 OpenClaw gateway」换成「in-process dispatch」。
 *
 * 其他保持：api-key 走 CLI 的分层解析（env/keychain/共享文件），锁/状态文件落 profile 目录。
 */
import { join } from "node:path";
import { RelayClient } from "../shared.js";
import type { DaemonLogger } from "./logger.js";
import type { StandaloneRuntime } from "./runtime.js";
import { RelayDispatcher } from "./relay-dispatcher.js";

const DEFAULT_HEARTBEAT_SEC = 10;
const DEFAULT_RECONNECT_BACKOFF_MS = 2000;

export interface RelayTunnelOptions {
  /** api-key label，用于状态文件与日志。 */
  label: string;
  /** Relay Server WebSocket 地址（config.relay.url，默认插件生产地址）。 */
  tunnelUrl: string;
  /** account 级 api-key（CLI resolveApiKey 解析所得）。 */
  apiKey: string;
  /** daemon 的 gateway 路由表，gateway-RPC 帧（type:"req"）in-process 派发用。 */
  runtime: StandaloneRuntime;
  /** daemon HTTP server base（http://127.0.0.1:port），HTTP-style frame（type:"request"）loopback 用。 */
  httpBaseUrl: string;
  /** loopback 时带的 gateway token。 */
  httpToken?: string;
  heartbeatSec?: number;
  reconnectBackoffMs?: number;
  /** profile 目录，用于落隧道状态文件与设备身份。 */
  stateDir: string;
  logger: DaemonLogger;
}

export interface RelayTunnelHandle {
  stop(reason: string): Promise<void>;
  status(): {
    connected: boolean;
    url: string;
    reconnectAttempt: number;
    lastDisconnectReason?: string;
  };
  /** 反向 push event 给手机端（取决于 Relay 服务端是否支持广播，预留接口）。 */
  pushEvent(event: string, payload: unknown): void;
}

function relayCredential(apiKey: string): { query: { apiKey: string } } {
  const raw = apiKey.startsWith("Bearer ") ? apiKey.slice("Bearer ".length) : apiKey;
  return { query: { apiKey: raw } };
}

/**
 * 启动 Relay 隧道。返回句柄用于查询状态 / 优雅停止 / 主动重连。
 * 非阻塞：connectWithAutoReconnect 在内部保持运行并自动重连。
 */
export function startRelayTunnel(opts: RelayTunnelOptions): RelayTunnelHandle {
  const { logger } = opts;
  const statusFilePath = join(opts.stateDir, "state", "tunnels", `${opts.label}.json`);

  let abortController = new AbortController();
  let connected = false;
  let reconnectAttempt = 0;
  let lastDisconnectReason: string | undefined;

  const client = new RelayClient({
    tunnelUrl: opts.tunnelUrl,
    credentialProvider: async () => relayCredential(opts.apiKey),
    heartbeatSec: opts.heartbeatSec ?? DEFAULT_HEARTBEAT_SEC,
    reconnectBackoffMs: opts.reconnectBackoffMs ?? DEFAULT_RECONNECT_BACKOFF_MS,
    statusFilePath,
    logger: {
      info: (m: string) => logger.info(`[tunnel:${opts.label}] ${m}`),
      warn: (m: string) => logger.warn(`[tunnel:${opts.label}] ${m}`),
      error: (m: string) => logger.error(`[tunnel:${opts.label}] ${m}`),
    },
  });

  const dispatcher = new RelayDispatcher({
    client,
    runtime: opts.runtime,
    logger,
    httpBaseUrl: opts.httpBaseUrl,
    httpToken: opts.httpToken,
    clientLabel: opts.label,
  });
  dispatcher.start();

  client.onConnected(() => {
    connected = true;
    lastDisconnectReason = undefined;
  });
  client.onDisconnected((reason) => {
    connected = false;
    reconnectAttempt += 1;
    lastDisconnectReason = reason;
  });

  const run = (): void => {
    client.connectWithAutoReconnect(abortController.signal).catch((err) => {
      logger.error(`Relay tunnel: 意外退出：${(err as Error).message}`);
    });
  };
  run();
  logger.info(`[tunnel:${opts.label}] Relay tunnel: 已启动（url=${opts.tunnelUrl} → in-process gateway dispatch）`);

  return {
    async stop(reason: string) {
      abortController.abort();
      await client.stop(reason);
    },
    status() {
      return {
        connected: connected || client.isConnected(),
        url: opts.tunnelUrl,
        reconnectAttempt,
        lastDisconnectReason,
      };
    },
    pushEvent(event: string, payload: unknown) {
      dispatcher.pushEvent(event, payload);
    },
  };
}

import {
  closeSync,
  mkdirSync,
  openSync,
  readFileSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { RelayClient } from "./relay-client.js";
import type { AuthProvider } from "../profile/types.js";
import { credentialsPath } from "../auth/credentials.js";
import { resolveHostStateDir, resolveStateDir } from "../host.js";
import { TunnelProxy } from "./proxy.js";
import { createConnectStatusReporter } from "./connect-status-reporter.js";
import type { UpdateInfo } from "../update/types.js";
import { redactUrlSecrets } from "./utils.js";

const DEFAULT_HEARTBEAT_SEC = 10;
const DEFAULT_RECONNECT_BACKOFF_MS = 2000;

interface TunnelServiceOptions {
  /** Relay Server WebSocket 地址（构建时从 RELAY_TUNNEL_URL 注入） */
  tunnelUrl: string;
  /** 认证 Provider：提供 ensureProvisioned 门 + relay 连接凭据。 */
  authProvider: AuthProvider;
  gatewayBaseUrl: string;
  /** Gateway 鉴权模式 */
  gatewayAuthMode?: "token" | "password";
  /** Gateway 鉴权 token */
  gatewayToken?: string;
  /** Gateway 鉴权密码 */
  gatewayPassword?: string;
  /** 心跳间隔（秒），默认 10 */
  heartbeatSec?: number;
  /** 重连基础退避时间（毫秒），默认 2000 */
  reconnectBackoffMs?: number;
  logger: {
    info: (msg: string) => void;
    warn: (msg: string) => void;
    error: (msg: string) => void;
  };
}

export interface RelayTunnelService {
  id: string;
  start(ctx: { stateDir: string }): Promise<void>;
  stop(): Promise<void>;
  notifyUpdateAvailable(update: UpdateInfo): void;
  clearPendingUpdate(): void;
  deactivateForExternalTunnel(reason: string): Promise<void>;
}

/**
 * 隧道服务：组装 RelayClient + TunnelProxy，作为 registerService 的入口。
 */
export function createTunnelService(
  opts: TunnelServiceOptions,
): RelayTunnelService {
  let client: RelayClient | null = null;
  let proxy: TunnelProxy | null = null;
  let abortController: AbortController | null = null;
  let provisionAbortController: AbortController | null = null;
  let lockFilePath: string | null = null;
  let lockFd: number | null = null;
  let pendingPluginUpdate: UpdateInfo | null = null;
  let suppressedByExternalTunnel = false;

  function emitPendingPluginUpdate(reason: string): void {
    const update = pendingPluginUpdate;
    if (!update || !client?.isConnected()) return;

    const frame = JSON.stringify({
      type: "event",
      event: "plugin.updateAvailable",
      payload: {
        pluginId: "phone-notifications",
        pluginName: "消息通知",
        current: update.current,
        latest: update.latest,
      },
    });
    opts.logger.info(
      `Relay tunnel: sending plugin.updateAvailable ${update.current} → ${update.latest} (${reason})`,
    );
    client.sendRaw(frame);
  }

  function isProcessAlive(pid: number): boolean {
    if (!Number.isInteger(pid) || pid <= 0) return false;
    if (pid === process.pid) return true;
    try {
      process.kill(pid, 0);
      return true;
    } catch (err: any) {
      return err?.code === "EPERM";
    }
  }

  function readLockOwner(filePath: string): number | null {
    try {
      const parsed = JSON.parse(readFileSync(filePath, "utf-8")) as {
        pid?: unknown;
      };
      return typeof parsed.pid === "number" ? parsed.pid : null;
    } catch {
      return null;
    }
  }

  function releaseLock(): void {
    const filePath = lockFilePath;
    const fd = lockFd;
    lockFilePath = null;
    lockFd = null;

    if (fd !== null) {
      try {
        closeSync(fd);
      } catch {
        // ignore
      }
    }
    if (filePath) {
      try {
        unlinkSync(filePath);
      } catch {
        // ignore
      }
    }
  }

  function acquireLock(filePath: string): boolean {
    mkdirSync(dirname(filePath), { recursive: true });

    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const fd = openSync(filePath, "wx", 0o600);
        writeFileSync(
          fd,
          JSON.stringify({
            pid: process.pid,
            startedAt: new Date().toISOString(),
            tunnelUrl: redactUrlSecrets(opts.tunnelUrl),
          }) + "\n",
        );
        lockFilePath = filePath;
        lockFd = fd;
        return true;
      } catch (err: any) {
        if (err?.code !== "EEXIST") {
          opts.logger.error(
            `Relay tunnel: failed to acquire local lock ${filePath}: ${String(err)}`,
          );
          return false;
        }

        const ownerPid = readLockOwner(filePath);
        if (ownerPid && !isProcessAlive(ownerPid)) {
          opts.logger.warn(
            `Relay tunnel: removing stale local lock owned by dead pid=${ownerPid}`,
          );
          try {
            unlinkSync(filePath);
          } catch {
            // ignore, retry below will fail if lock is still present
          }
          continue;
        }

        opts.logger.warn(
          `Relay tunnel: another local process already owns the tunnel lock${ownerPid ? ` (pid=${ownerPid})` : ""}; skipping connection to avoid duplicate token sessions`,
        );
        return false;
      }
    }

    return false;
  }

  async function shutdownRuntime(reason: string): Promise<void> {
    if (provisionAbortController) {
      provisionAbortController.abort();
      provisionAbortController = null;
    }
    await client?.stop(reason);
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
    releaseLock();
    proxy?.cleanup();
    proxy = null;
    client = null;
  }

  return {
    id: "relay-tunnel",

    async start(ctx: { stateDir: string }) {
      if (suppressedByExternalTunnel) {
        opts.logger.info(
          "Relay tunnel: external tunnel currently owns ingress; skipping relay startup",
        );
        return;
      }

      // 先清理上一轮连接，防止 start() 被重复调用时产生多个 RelayClient 互踢
      if (abortController) {
        opts.logger.info("Relay tunnel: aborting previous connection before restart");
        await shutdownRuntime("service restarting");
      }

      // provision 门：openclaw/arkclaw 为 no-op；jvsclaw 在此重试置换 apiKey 直到成功。
      // stop() 会中止此重试，避免置换长时间失败时 start() 永远挂起。
      provisionAbortController = new AbortController();
      await opts.authProvider.ensureProvisioned(provisionAbortController.signal);

      const authStatus = await opts.authProvider.status();
      if (!authStatus.ready) {
        opts.logger.warn(
          `Relay tunnel: 认证未就绪（${authStatus.reason ?? "n/a"}，credentials=${credentialsPath()}），跳过隧道连接。请执行 ntf auth set-api-key <apiKey>`,
        );
        return;
      }

      const { logger } = opts;
      const hostStateDir = resolveHostStateDir(ctx.stateDir);
      const stateDir = resolveStateDir(hostStateDir);
      const baseStateDir = join(stateDir, "plugins", "phone-notifications");
      logger.info(
        `Relay tunnel: starting (pid=${process.pid}, url=${redactUrlSecrets(opts.tunnelUrl)}, heartbeat=${opts.heartbeatSec ?? DEFAULT_HEARTBEAT_SEC}s, backoff=${opts.reconnectBackoffMs ?? DEFAULT_RECONNECT_BACKOFF_MS}ms, gateway=${opts.gatewayBaseUrl}, hasGatewayToken=${!!opts.gatewayToken}, hasGatewayPwd=${!!opts.gatewayPassword})`,
      );
      const statusFilePath = join(baseStateDir, "tunnel-status.json");
      const lockPath = join(baseStateDir, "relay-tunnel.lock");

      if (!acquireLock(lockPath)) {
        return;
      }

      try {
        client = new RelayClient({
          tunnelUrl: opts.tunnelUrl,
          credentialProvider: () => opts.authProvider.getRelayCredential(),
          heartbeatSec: opts.heartbeatSec ?? DEFAULT_HEARTBEAT_SEC,
          reconnectBackoffMs: opts.reconnectBackoffMs ?? DEFAULT_RECONNECT_BACKOFF_MS,
          statusFilePath,
          logger,
        });

        proxy = new TunnelProxy({
          stateDir: baseStateDir,
          hostStateDir,
          gatewayBaseUrl: opts.gatewayBaseUrl,
          gatewayAuthMode: opts.gatewayAuthMode,
          gatewayToken: opts.gatewayToken,
          gatewayPassword: opts.gatewayPassword,
          client,
          logger,
        });

        const connectStatusReporter = createConnectStatusReporter({ logger });

        client.onInbound((frame) => proxy!.handleFrame(frame));
        client.onConnected(() => {
          connectStatusReporter.report("connected");
          emitPendingPluginUpdate("relay connected");
        });
        client.onDisconnected((reason) => {
          logger.warn(
            `Relay tunnel: relay disconnected, cleaning local proxy state (${reason})`,
          );
          connectStatusReporter.report("disconnected");
          proxy?.cleanup();
        });

        abortController = new AbortController();
        // 非阻塞启动，connectWithAutoReconnect 内部会保持运行
        client.connectWithAutoReconnect(abortController.signal).catch((err) => {
          releaseLock();
          logger.error(`Relay tunnel: unexpected error: ${err}`);
        });

        logger.info("Relay tunnel 服务已启动");
      } catch (err) {
        releaseLock();
        throw err;
      }
    },

    async stop() {
      suppressedByExternalTunnel = false;
      await shutdownRuntime("service stopping");
      opts.logger.info("Relay tunnel 服务已停止");
    },

    async deactivateForExternalTunnel(reason: string) {
      if (suppressedByExternalTunnel && !client && !proxy && !abortController) {
        return;
      }

      suppressedByExternalTunnel = true;
      opts.logger.info(
        `Relay tunnel: external tunnel claimed ingress (${reason}), shutting down relay connection`,
      );
      await shutdownRuntime("external tunnel active");
    },

    notifyUpdateAvailable(update: UpdateInfo) {
      pendingPluginUpdate = update;
      emitPendingPluginUpdate("update detected");
    },

    clearPendingUpdate() {
      pendingPluginUpdate = null;
    },
  };
}

import { writeFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";
import WebSocket from "ws";
import type { InboundFrame, OutboundFrame } from "./types.js";
import { maskSecret, redactUrlSecrets } from "./utils.js";
import type { AuthCredential } from "../profile/types.js";

/** 每次（重）连接时获取最新连接凭据；jvsclaw 等可在此透明刷新 token。 */
export type CredentialProvider = () => Promise<AuthCredential>;

function previewText(text: string, max = 500): string {
  return text.length <= max ? text : `${text.substring(0, max)}…`;
}

/** 从凭据里挑一个代表性密文用于脱敏日志（优先 query.apiKey，其次首个 header 值）。 */
function maskableSecret(credential: AuthCredential): string {
  return (
    credential.query?.apiKey ??
    Object.values(credential.headers ?? {})[0] ??
    ""
  );
}

/** WebSocket Upgrade 握手超时（毫秒）。云上常见 NAT/中间盒吞包场景下兜底用。 */
const HANDSHAKE_TIMEOUT_MS = 15_000;
/** 兜底 watchdog：如果握手超时也没触发，强制 terminate 进入重连路径。 */
const CONNECT_WATCHDOG_MS = 20_000;
/** 断线期间发送会非常密集，限频避免把真正的断线原因刷掉。 */
const SEND_SKIPPED_LOG_INTERVAL_MS = 30_000;

/** 持久化到磁盘的隧道连接状态 */
export interface TunnelStatusInfo {
  /** 当前连接状态 */
  state: "connected" | "disconnected" | "connecting" | "stopped";
  /** 状态变更时间 ISO */
  since: string;
  /** 累计重连次数 */
  reconnectAttempt: number;
  /** 上次断开原因（仅 disconnected 时有值） */
  lastDisconnectReason?: string;
}

export interface RelayClientOptions {
  tunnelUrl: string;
  /** 每次连接时获取连接凭据（query/headers）；支持 token 刷新。 */
  credentialProvider: CredentialProvider;
  heartbeatSec: number;
  reconnectBackoffMs: number;
  /** 状态文件路径，设置后会在连接状态变更时写入 */
  statusFilePath?: string;
  logger: {
    info: (msg: string) => void;
    warn: (msg: string) => void;
    error: (msg: string) => void;
  };
}

export type InboundHandler = (frame: InboundFrame) => void | Promise<void>;
export type ConnectedHandler = () => void | Promise<void>;
export type DisconnectedHandler = (reason: string) => void | Promise<void>;

/**
 * Relay WebSocket 客户端。
 * 负责建连、token 认证、心跳保活、断线指数退避重连。
 */
export class RelayClient {
  private ws: WebSocket | null = null;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectAttempt = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private handlers: InboundHandler[] = [];
  private connectedHandlers: ConnectedHandler[] = [];
  private disconnectedHandlers: DisconnectedHandler[] = [];
  private aborted = false;
  private lastInboundAt = 0;
  private stopPromise: Promise<void> | null = null;
  private skippedSendLogLastAt: number | null = null;
  private skippedSendLogSuppressed = 0;

  constructor(private readonly opts: RelayClientOptions) {}

  /** 写连接状态到磁盘 */
  private writeStatus(
    state: TunnelStatusInfo["state"],
    lastDisconnectReason?: string,
  ): void {
    if (!this.opts.statusFilePath) return;
    const info: TunnelStatusInfo = {
      state,
      since: new Date().toISOString(),
      reconnectAttempt: this.reconnectAttempt,
      lastDisconnectReason,
    };
    try {
      mkdirSync(dirname(this.opts.statusFilePath), { recursive: true });
      writeFileSync(this.opts.statusFilePath, JSON.stringify(info, null, 2));
    } catch {
      // 写状态文件失败不影响主流程
    }
  }

  /** 注册入站帧处理函数 */
  onInbound(handler: InboundHandler): void {
    this.handlers.push(handler);
  }

  /** 注册连接成功后的回调 */
  onConnected(handler: ConnectedHandler): void {
    this.connectedHandlers.push(handler);
  }

  /** 注册 Relay 连接断开后的回调 */
  onDisconnected(handler: DisconnectedHandler): void {
    this.disconnectedHandlers.push(handler);
  }

  /** 当前是否已连上 Relay */
  isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /** 优雅停止客户端，优先发送 close 帧，超时后再强制 terminate */
  async stop(reason = "client stopping"): Promise<void> {
    if (this.stopPromise) {
      return this.stopPromise;
    }

    this.aborted = true;
    this.writeStatus("stopped");
    this.stopHeartbeat();
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    const ws = this.ws;
    this.ws = null;
    if (!ws) {
      return;
    }

    this.opts.logger.info(
      `Relay tunnel: stopping client (readyState=${ws.readyState}, reason=${reason})`,
    );

    if (
      ws.readyState === WebSocket.CLOSED ||
      ws.readyState === WebSocket.CLOSING
    ) {
      return;
    }

    if (ws.readyState !== WebSocket.OPEN) {
      try {
        ws.terminate();
      } catch {
        // ignore
      }
      return;
    }

    this.stopPromise = new Promise<void>((resolve) => {
      let finished = false;
      let fallbackTimer: ReturnType<typeof setTimeout> | null = null;

      const finish = () => {
        if (finished) return;
        finished = true;
        if (fallbackTimer) {
          clearTimeout(fallbackTimer);
          fallbackTimer = null;
        }
        ws.off("close", handleClose);
        ws.off("error", handleError);
        this.stopPromise = null;
        resolve();
      };

      const handleClose = () => finish();
      const handleError = () => finish();

      ws.once("close", handleClose);
      ws.once("error", handleError);

      fallbackTimer = setTimeout(() => {
        this.opts.logger.warn(
          "Relay tunnel: graceful close timed out, forcing terminate",
        );
        try {
          ws.terminate();
        } catch {
          // ignore
        }
        finish();
      }, 1500);

      try {
        ws.close(1000, reason);
      } catch {
        finish();
      }
    });

    await this.stopPromise;
  }

  /** 发送出站帧 */
  send(frame: OutboundFrame): void {
    const ws = this.ws;
    if (ws?.readyState === WebSocket.OPEN) {
      const payload = JSON.stringify(frame);
      this.opts.logger.info(
        `Relay tunnel: ▶ send frame type=${frame.type}, id=${"id" in frame ? frame.id : "N/A"} (${payload.length} chars)`,
      );
      try {
        ws.send(payload);
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        this.opts.logger.warn(
          `Relay tunnel: send failed, forcing reconnect: ${message}`,
        );
        this.forceReconnectFromSocket(ws, `send-failed: ${message}`);
      }
    } else {
      this.logSendSkipped("send", `frame type=${frame.type}`);
    }
  }

  /** 原样透传文本到 Relay（用于 Gateway WS 响应直接回传） */
  sendRaw(text: string): void {
    const ws = this.ws;
    if (ws?.readyState === WebSocket.OPEN) {
      this.opts.logger.info(
        `Relay tunnel: ▶ sendRaw (${text.length} chars): ${text.length <= 500 ? text : text.substring(0, 500) + "…"}`,
      );
      try {
        ws.send(text);
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        this.opts.logger.warn(
          `Relay tunnel: sendRaw failed, forcing reconnect: ${message}`,
        );
        this.forceReconnectFromSocket(ws, `sendRaw-failed: ${message}`);
      }
    } else {
      this.logSendSkipped("sendRaw");
    }
  }

  /** 启动连接，带自动重连，直到 abortSignal 触发 */
  async connectWithAutoReconnect(abortSignal?: AbortSignal): Promise<void> {
    if (abortSignal) {
      abortSignal.addEventListener(
        "abort",
        () => {
          void this.stop();
        },
        { once: true },
      );
    }

    await this.connect();

    // 保持运行直到 abort
    return new Promise<void>((resolve) => {
      if (abortSignal) {
        abortSignal.addEventListener("abort", () => resolve(), { once: true });
      }
    });
  }

  private async connect(): Promise<void> {
    if (this.aborted) return;

    // 确保旧连接和定时器都被清理，防止重复连接
    this.cleanup(true);

    let credential: AuthCredential;
    try {
      credential = await this.opts.credentialProvider();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      this.opts.logger.warn(
        `Relay tunnel: failed to resolve credential, scheduling reconnect: ${message}`,
      );
      this.writeStatus("disconnected", `credential-error: ${message}`);
      this.scheduleReconnect();
      return;
    }

    this.opts.logger.info(
      `Relay tunnel: connecting to ${redactUrlSecrets(this.opts.tunnelUrl)} (attempt=${this.reconnectAttempt}, heartbeat=${this.opts.heartbeatSec}s, apiKey=${maskSecret(maskableSecret(credential))})`,
    );
    this.writeStatus("connecting");

    return new Promise<void>((resolve) => {
      let settled = false;
      const settle = () => {
        if (!settled) {
          settled = true;
          resolve();
        }
      };

      const wsUrl = new URL(this.opts.tunnelUrl);
      // 凭据中的 query 参数（如 apiKey）拼到连接 URL；已显式带上的不覆盖。
      for (const [key, value] of Object.entries(credential.query ?? {})) {
        if (!wsUrl.searchParams.get(key)) {
          wsUrl.searchParams.set(key, value);
        }
      }

      const loggableUrl = new URL(wsUrl.toString());
      const apiKey = loggableUrl.searchParams.get("apiKey");
      if (apiKey) {
        loggableUrl.searchParams.set(
          "apiKey",
          apiKey.length > 8 ? `${apiKey.substring(0, 8)}…` : "…",
        );
      }
      this.opts.logger.info(
        `Relay tunnel: connecting to ${loggableUrl.toString()} (attempt=${this.reconnectAttempt + 1})`,
      );

      const ws = new WebSocket(wsUrl.toString(), {
        headers: credential.headers ?? {},
        // 防止 TCP 通但 HTTP Upgrade 响应被吞导致永久挂在 CONNECTING
        handshakeTimeout: HANDSHAKE_TIMEOUT_MS,
      });
      // 将握手中的 socket 也记为当前连接，确保首连失败/握手前断开也能触发重连。
      this.ws = ws;

      // 兜底 watchdog：即使 handshakeTimeout 因某些原因失效，也强制 terminate 走重连路径。
      let connectWatchdog: ReturnType<typeof setTimeout> | null = setTimeout(
        () => {
          connectWatchdog = null;
          if (this.ws !== ws || ws.readyState === WebSocket.OPEN) {
            return;
          }
          this.opts.logger.warn(
            `Relay tunnel: connect watchdog fired (readyState=${ws.readyState}, attempt=${this.reconnectAttempt}), forcing reconnect`,
          );
          this.forceReconnectFromSocket(ws, "connect-watchdog-timeout");
          settle();
        },
        CONNECT_WATCHDOG_MS,
      );

      const clearConnectWatchdog = () => {
        if (connectWatchdog) {
          clearTimeout(connectWatchdog);
          connectWatchdog = null;
        }
      };

      ws.on("open", () => {
        clearConnectWatchdog();
        if (this.aborted || this.ws !== ws) {
          this.opts.logger.warn(
            `Relay tunnel: open fired but aborted=${this.aborted}, stale=${this.ws !== ws}, closing`,
          );
          try {
            ws.terminate();
          } catch {
            // ignore
          }
          settle();
          return;
        }
        this.reconnectAttempt = 0;
        this.lastInboundAt = Date.now();
        this.startHeartbeat();
        this.opts.logger.info("Relay tunnel: ✔ connected, heartbeat started");
        this.writeStatus("connected");
        this.emitConnected();
        settle();
      });

      ws.on("pong", () => {
        if (this.ws === ws) {
          this.opts.logger.info("Relay tunnel: ← pong received");
          this.markInboundActivity();
        }
      });

      ws.on("message", (data: WebSocket.RawData) => {
        if (this.ws !== ws) {
          return;
        }
        this.handleMessage(data);
      });

      ws.on("close", (code: number, reason: Buffer) => {
        clearConnectWatchdog();
        const reasonStr = reason.toString();
        const lastInboundAgoMs = this.lastInboundAt
          ? Date.now() - this.lastInboundAt
          : null;
        const isCurrentSocket = this.ws === ws;
        const logMessage =
          `Relay tunnel: disconnected (code=${code}, reason=${previewText(reasonStr, 200)}, lastInboundAgoMs=${lastInboundAgoMs ?? "N/A"}, reconnectAttempt=${this.reconnectAttempt})`;
        if (this.aborted || !isCurrentSocket) {
          this.opts.logger.info(logMessage);
        } else {
          this.opts.logger.warn(logMessage);
        }
        if (isCurrentSocket) {
          this.stopHeartbeat();
          this.ws = null;
          const disconnectReason = `code=${code}, reason=${reasonStr}`;
          this.writeStatus("disconnected", disconnectReason);
          this.emitDisconnected(disconnectReason);
          this.scheduleReconnect();
        }
        settle();
      });

      ws.on("error", (err: Error) => {
        clearConnectWatchdog();
        this.opts.logger.error(
          `Relay tunnel: WebSocket error: ${err.message} (readyState=${ws.readyState}, reconnectAttempt=${this.reconnectAttempt}, url=${redactUrlSecrets(wsUrl.toString())})`,
        );
        // 多数情况 error 后 ws 会再触发 close；但部分底层错误只 error 不 close，
        // 这里幂等地兜底一次 scheduleReconnect，scheduleReconnect 内部对已排程的重连会跳过。
        if (this.ws === ws) {
          this.forceReconnectFromSocket(ws, `error: ${err.message}`);
        }
        settle();
      });
    });
  }

  private emitConnected(): void {
    for (const handler of this.connectedHandlers) {
      Promise.resolve(handler()).catch((err) => {
        this.opts.logger.warn(
          `Relay tunnel: onConnected handler failed: ${err instanceof Error ? err.message : String(err)}`,
        );
      });
    }
  }

  private emitDisconnected(reason: string): void {
    for (const handler of this.disconnectedHandlers) {
      Promise.resolve(handler(reason)).catch((err) => {
        this.opts.logger.warn(
          `Relay tunnel: onDisconnected handler failed: ${err instanceof Error ? err.message : String(err)}`,
        );
      });
    }
  }

  private handleMessage(data: WebSocket.RawData): void {
    const text =
      typeof data === "string"
        ? data
        : Buffer.isBuffer(data)
          ? data.toString()
          : String(data);

    this.markInboundActivity();

    // 应用层心跳 pong 直接消费，不走帧解析
    if (text === "pong") {
      return;
    }

    this.opts.logger.info(
      `Relay tunnel: ★ received message (${text.length} chars): ${previewText(text)}`,
    );

    let frame: InboundFrame;
    try {
      frame = JSON.parse(text) as InboundFrame;
    } catch {
      this.opts.logger.warn(
        `Relay tunnel: received invalid frame, ignoring (preview=${previewText(text, 200)})`,
      );
      return;
    }

    this.opts.logger.info(`Relay tunnel: parsed frame type=${frame.type}, id=${"id" in frame ? frame.id : "N/A"}`);

    for (const handler of this.handlers) {
      try {
        const result = handler(frame);
        if (result instanceof Promise) {
          result.catch((err) => {
            this.opts.logger.error(`Relay tunnel: handler error: ${err}`);
          });
        }
      } catch (err) {
        this.opts.logger.error(`Relay tunnel: handler error: ${err}`);
      }
    }
  }

  private startHeartbeat(): void {
    this.stopHeartbeat();
    const intervalMs = this.opts.heartbeatSec * 1000;
    this.sendHeartbeat();
    this.heartbeatTimer = setInterval(() => {
      this.sendHeartbeat();
    }, intervalMs);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
  }

  private getHeartbeatTimeoutMs(): number {
    return this.opts.heartbeatSec * 3 * 1000;
  }

  private sendHeartbeat(): void {
    const ws = this.ws;
    if (ws?.readyState !== WebSocket.OPEN) {
      return;
    }

    const idleMs = Date.now() - this.lastInboundAt;
    const timeoutMs = this.getHeartbeatTimeoutMs();
    if (idleMs >= timeoutMs) {
      this.opts.logger.warn(
        `Relay tunnel: heartbeat timeout, no inbound activity for ${idleMs}ms (threshold=${timeoutMs}ms)`,
      );
      this.forceReconnectFromSocket(ws, `heartbeat-timeout idleMs=${idleMs}`);
      return;
    }

    this.opts.logger.info('Relay tunnel: → heartbeat "ping"');
    try {
      ws.send("ping");
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      this.opts.logger.warn(
        `Relay tunnel: heartbeat text ping failed, forcing reconnect: ${message}`,
      );
      this.forceReconnectFromSocket(ws, `heartbeat-send-failed: ${message}`);
      return;
    }
    try {
      ws.ping();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      this.opts.logger.warn(
        `Relay tunnel: heartbeat control ping failed, forcing reconnect: ${message}`,
      );
      this.forceReconnectFromSocket(ws, `heartbeat-ping-failed: ${message}`);
    }
  }

  private markInboundActivity(): void {
    this.lastInboundAt = Date.now();
  }

  private cleanup(force = false): void {
    this.opts.logger.info(
      `Relay tunnel: cleanup (ws=${this.ws ? "open" : "null"}, heartbeat=${!!this.heartbeatTimer}, reconnect=${!!this.reconnectTimer})`,
    );
    this.stopHeartbeat();
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      try {
        if (force) {
          this.ws.terminate();
        } else {
          this.ws.close();
        }
      } catch {
        // ignore
      }
      this.ws = null;
    }
  }

  private forceReconnectFromSocket(ws: WebSocket, reason: string): void {
    if (this.aborted || this.ws !== ws) return;

    this.stopHeartbeat();
    this.ws = null;
    this.writeStatus("disconnected", reason);
    this.emitDisconnected(reason);
    this.scheduleReconnect();

    try {
      if (ws.readyState !== WebSocket.CLOSED) {
        ws.terminate();
      }
    } catch {
      // ignore
    }
  }

  private logSendSkipped(kind: "send" | "sendRaw", detail?: string): void {
    const now = Date.now();
    const shouldLog =
      this.skippedSendLogLastAt === null ||
      now - this.skippedSendLogLastAt >= SEND_SKIPPED_LOG_INTERVAL_MS;

    if (!shouldLog) {
      this.skippedSendLogSuppressed++;
      return;
    }

    const suppressedSuffix =
      this.skippedSendLogSuppressed > 0
        ? `, suppressed=${this.skippedSendLogSuppressed}`
        : "";
    const detailSuffix = detail ? `, ${detail}` : "";
    this.opts.logger.warn(
      `Relay tunnel: ▶ ${kind} skipped, ws not open (readyState=${this.ws?.readyState ?? "null"}${detailSuffix}${suppressedSuffix})`,
    );
    this.skippedSendLogLastAt = now;
    this.skippedSendLogSuppressed = 0;
  }

  private scheduleReconnect(): void {
    if (this.aborted) return;

    // 已经排了一次重连就不再重排，避免 error+close 双触发或 watchdog 与 close 双触发时
    // attempt 被多扣、退避被无意义重置。
    if (this.reconnectTimer) {
      return;
    }

    const baseMs = this.opts.reconnectBackoffMs;
    const delayMs = Math.min(
      baseMs * Math.pow(2, this.reconnectAttempt),
      60_000,
    );
    this.reconnectAttempt++;

    this.opts.logger.info(
      `Relay tunnel: reconnecting in ${delayMs}ms (attempt ${this.reconnectAttempt})`,
    );

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (!this.aborted) {
        this.connect().catch((err) => {
          this.opts.logger.error(`Relay tunnel: reconnect failed: ${err}`);
        });
      }
    }, delayMs);
  }
}

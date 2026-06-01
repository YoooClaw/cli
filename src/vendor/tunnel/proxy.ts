import { randomUUID } from "node:crypto";
import WebSocket from "ws";
import type { RelayClient } from "./relay-client.js";
import type { ReqFrame, InboundFrame, RequestFrame } from "./types.js";
import { previewText } from "./utils.js";
import { resolveHostStateDir } from "../host.js";
import {
  type DeviceIdentity,
  resolveClientStateDir,
  loadOrCreateDeviceIdentity,
  loadDeviceAuthToken,
  storeDeviceAuthToken,
  clearDeviceAuthToken,
  buildDeviceAuthPayload,
  signDevicePayload,
  publicKeyRawBase64UrlFromPem,
} from "./device-identity.js";
import { handleHttpRequest } from "./http-proxy.js";
import { WsProxy } from "./ws-proxy.js";

// ─── Types ───

export const RELAY_TUNNEL_GATEWAY_CLIENT_INSTANCE_ID =
  "phone-notifications-relay-tunnel";

const GATEWAY_OPERATOR_SCOPES = [
  "operator.admin",
  "operator.approvals",
  "operator.pairing",
  "operator.read",
  "operator.write",
] as const;

// openclaw 2026.5.12 起本地 gateway RPC 要求 protocol >= 4；保留 3 以兼容老网关。
const GATEWAY_RPC_MIN_PROTOCOL = 3;
const GATEWAY_RPC_MAX_PROTOCOL = 4;

interface ProxyOptions {
  stateDir?: string;
  hostStateDir?: string;
  gatewayBaseUrl: string;
  /** Gateway 鉴权模式 */
  gatewayAuthMode?: "token" | "password";
  /** Gateway 鉴权 token，代理请求到本地 gateway 时使用 */
  gatewayToken?: string;
  /** Gateway 鉴权密码，代理请求到本地 gateway 时使用 */
  gatewayPassword?: string;
  client: RelayClient;
  logger: {
    info: (msg: string) => void;
    warn: (msg: string) => void;
    error: (msg: string) => void;
  };
}

const MAX_AUTO_PAIRING_APPROVALS = 3;
const RECENT_ABORTED_CHAT_RUN_TTL_MS = 60_000;
const GATEWAY_RPC_HANDSHAKE_TIMEOUT_MS = 10_000;
const MAX_GATEWAY_WS_PENDING = 200;

interface PendingGatewayReqMeta {
  method: string;
  sessionKey?: string;
}

interface PendingGatewayPayload {
  payload: string;
  reqId?: string;
}

interface RecentAbortedChatRun {
  sessionKey: string;
  abortedAtMs: number;
}

type ApproveDevicePairingFn = (
  requestId: string,
  hostStateDir?: string,
) => Promise<unknown>;

let approveDevicePairingPromise: Promise<ApproveDevicePairingFn | null> | null =
  null;
let approveDevicePairingWarned = false;

function formatErrorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return String(err);
}

async function loadApproveDevicePairing(
  logger: ProxyOptions["logger"],
): Promise<ApproveDevicePairingFn | null> {
  if (!approveDevicePairingPromise) {
    approveDevicePairingPromise = import("openclaw/plugin-sdk/device-bootstrap")
      .then((mod) =>
        typeof mod.approveDevicePairing === "function"
          ? (mod.approveDevicePairing as ApproveDevicePairingFn)
          : null,
      )
      .catch((err: unknown) => {
        if (!approveDevicePairingWarned) {
          approveDevicePairingWarned = true;
          logger.warn(
            `TunnelProxy: local gateway auto-pairing disabled because current OpenClaw runtime does not expose device bootstrap SDK (${formatErrorMessage(err)})`,
          );
        }
        return null;
      });
  }

  const approveDevicePairing = await approveDevicePairingPromise;
  if (!approveDevicePairing && !approveDevicePairingWarned) {
    approveDevicePairingWarned = true;
    logger.warn(
      "TunnelProxy: local gateway auto-pairing disabled because current OpenClaw runtime does not expose device bootstrap SDK",
    );
  }
  return approveDevicePairing;
}

/**
 * 透明代理：接收 Relay 转发的请求帧，代理到本地 gateway，回传响应帧。
 * 支持 HTTP 请求代理和 WebSocket 连接代理。
 */
export class TunnelProxy {
  /** 到本地 Gateway 的持久 RPC WebSocket 连接 */
  private gatewayWs: WebSocket | null = null;
  private gatewayWsConnecting = false;
  /** 握手是否已完成（收到 hello-ok） */
  private gatewayWsReady = false;
  /** 收到本地自动配对成功后，在 close 回调里触发重连 */
  private gatewayWsReconnectRequested = false;
  /** 本地自动配对审批进行中，期间 Gateway 关闭连接时先保留 pending 请求。 */
  private gatewayWsPairingApprovalPending = false;
  /** 防止配对失败时无限重连 */
  private gatewayWsAutoPairingApprovals = 0;
  /** 等待 Gateway WS 握手完成后发送的帧队列 */
  private gatewayWsPending: PendingGatewayPayload[] = [];
  /** 记录已转发到 Gateway 的 req 元信息，便于回包时做定向处理。 */
  private gatewayReqMetaById = new Map<string, PendingGatewayReqMeta>();
  /** 最近由用户手动中断的 chat.run，用于过滤 runtime 误补发的 synthetic failure。 */
  private recentAbortedChatRuns = new Map<string, RecentAbortedChatRun>();
  /** Relay 断开或服务停止时要中断的 HTTP 代理请求。 */
  private httpAbortControllers = new Map<string, AbortController>();

  /** 设备身份，用于 Gateway connect 握手 */
  private deviceIdentity: DeviceIdentity;

  private readonly stateDir: string;
  private readonly hostStateDir: string;
  private readonly wsProxy: WsProxy;

  constructor(private readonly opts: ProxyOptions) {
    this.stateDir = resolveClientStateDir(opts.stateDir);
    this.hostStateDir = opts.hostStateDir ?? resolveHostStateDir();
    this.deviceIdentity = loadOrCreateDeviceIdentity(this.stateDir);
    opts.logger.info(
      `TunnelProxy: loaded device identity (deviceId=${this.deviceIdentity.deviceId})`,
    );
    this.wsProxy = new WsProxy({
      gatewayBaseUrl: opts.gatewayBaseUrl,
      client: opts.client,
      logger: opts.logger,
    });
  }

  // ─── Frame dispatcher ───

  /** 处理从 Relay 收到的入站帧 */
  async handleFrame(frame: InboundFrame): Promise<void> {
    this.opts.logger.info(
      `TunnelProxy: handling frame type=${frame.type}, id=${"id" in frame ? frame.id : "N/A"}`,
    );
    switch (frame.type) {
      case "request":
        await this.handleRequestFrame(frame);
        break;
      case "req":
        this.handleReqFrame(frame);
        break;
      case "ws_open":
        this.wsProxy.handleWsOpen(frame);
        break;
      case "ws_data":
        this.wsProxy.handleWsData(frame);
        break;
      case "ws_close":
        this.wsProxy.handleWsClose(frame);
        break;
      default:
        this.opts.logger.warn(
          `TunnelProxy: unknown frame type="${(frame as any).type}", id=${"id" in frame ? (frame as any).id : "N/A"}, forwarding raw to gateway`,
        );
        // 未知帧类型也尝试通过 RPC WS 转发，避免静默丢弃
        this.forwardRawToGateway(JSON.stringify(frame));
        break;
    }
  }

  /** 清理所有代理的 WebSocket 连接 */
  cleanup(): void {
    this.opts.logger.info(
      `TunnelProxy: cleanup, closing ${this.wsProxy.activeCount} active WS connections, gatewayWs=${!!this.gatewayWs}`,
    );
    this.wsProxy.cleanup();
    for (const [id, controller] of this.httpAbortControllers) {
      this.opts.logger.info(`TunnelProxy: aborting HTTP proxy id=${id}`);
      controller.abort();
    }
    this.httpAbortControllers.clear();

    const gatewayWs = this.gatewayWs;
    this.gatewayWs = null;
    this.gatewayWsReady = false;
    this.gatewayWsConnecting = false;
    this.gatewayWsReconnectRequested = false;
    this.gatewayWsPairingApprovalPending = false;
    this.gatewayWsAutoPairingApprovals = 0;
    this.gatewayWsPending = [];
    this.gatewayReqMetaById.clear();
    this.recentAbortedChatRuns.clear();
    if (gatewayWs) {
      try {
        gatewayWs.close();
      } catch {
        // ignore
      }
    }
  }

  // ─── Gateway RPC WebSocket ───

  private async handleRequestFrame(frame: RequestFrame): Promise<void> {
    const controller = new AbortController();
    this.httpAbortControllers.set(frame.id, controller);
    try {
      await handleHttpRequest(
        {
          ...this.opts,
          abortSignal: controller.signal,
        },
        frame,
      );
    } finally {
      this.httpAbortControllers.delete(frame.id);
    }
  }

  private pushGatewayPending(
    entry: PendingGatewayPayload,
    reason: string,
  ): boolean {
    if (this.gatewayWsPending.length >= MAX_GATEWAY_WS_PENDING) {
      this.opts.logger.error(
        `TunnelProxy: gateway WS pending queue overflow (${this.gatewayWsPending.length}/${MAX_GATEWAY_WS_PENDING}); failing ${entry.reqId ? `req id=${entry.reqId}` : "raw frame"}`,
      );
      this.failGatewayPendingEntry(
        entry,
        "GATEWAY_RPC_QUEUE_FULL",
        "local gateway RPC queue is full",
      );
      return false;
    }

    this.gatewayWsPending.push(entry);
    this.opts.logger.info(
      `TunnelProxy: gateway WS pending queue size=${this.gatewayWsPending.length} (${reason})`,
    );
    return true;
  }

  /** 将未知帧原样转发到 Gateway WS */
  private forwardRawToGateway(payload: string): void {
    if (this.gatewayWsReady && this.gatewayWs?.readyState === WebSocket.OPEN) {
      this.sendGatewayPayload(this.gatewayWs, { payload }, "raw frame");
    } else {
      if (
        this.pushGatewayPending(
          { payload },
          "raw frame queued before gateway WS ready",
        )
      ) {
        this.ensureGatewayWs();
      }
    }
  }

  /** 处理 Relay 转发的 Gateway RPC 请求帧，原样通过 WebSocket 发给本地 Gateway */
  private handleReqFrame(frame: ReqFrame): void {
    const payload = JSON.stringify(frame);
    this.gatewayReqMetaById.set(frame.id, {
      method: frame.method,
      sessionKey: this.normalizeSessionKey(frame.params?.sessionKey),
    });
    this.opts.logger.info(
      `TunnelProxy: req id=${frame.id} method=${frame.method} → gateway WS (${payload.length} chars)`,
    );

    if (this.gatewayWsReady && this.gatewayWs?.readyState === WebSocket.OPEN) {
      this.sendGatewayPayload(
        this.gatewayWs,
        { payload, reqId: frame.id },
        `req id=${frame.id}`,
      );
    } else {
      this.opts.logger.info(
        `TunnelProxy: req id=${frame.id} queued, gateway WS not ready yet`,
      );
      if (
        this.pushGatewayPending(
          { payload, reqId: frame.id },
          `req id=${frame.id} queued before gateway WS handshake`,
        )
      ) {
        this.ensureGatewayWs();
      }
    }
  }

  private sendGatewayRpcError(
    id: string,
    code: string,
    message: string,
  ): void {
    this.gatewayReqMetaById.delete(id);
    this.opts.client.sendRaw(
      JSON.stringify({
        type: "res",
        id,
        ok: false,
        error: {
          code,
          message,
        },
      }),
    );
  }

  private failGatewayPendingEntry(
    entry: PendingGatewayPayload,
    code: string,
    message: string,
  ): void {
    if (!entry.reqId) return;
    this.sendGatewayRpcError(entry.reqId, code, message);
  }

  private failGatewayPending(code: string, message: string): void {
    if (this.gatewayWsPending.length === 0) return;
    const pending = this.gatewayWsPending;
    this.gatewayWsPending = [];
    this.opts.logger.warn(
      `TunnelProxy: failing ${pending.length} pending gateway RPC frame(s): ${message}`,
    );
    for (const entry of pending) {
      this.failGatewayPendingEntry(entry, code, message);
    }
  }

  private failGatewayInflight(code: string, message: string): void {
    if (this.gatewayReqMetaById.size === 0) return;
    const ids = [...this.gatewayReqMetaById.keys()];
    this.opts.logger.warn(
      `TunnelProxy: failing ${ids.length} in-flight gateway RPC request(s): ${message}`,
    );
    for (const id of ids) {
      this.sendGatewayRpcError(id, code, message);
    }
  }

  private failAllGatewayRequests(code: string, message: string): void {
    this.failGatewayPending(code, message);
    this.failGatewayInflight(code, message);
  }

  private sendGatewayPayload(
    ws: WebSocket,
    entry: PendingGatewayPayload,
    context: string,
  ): boolean {
    try {
      ws.send(entry.payload);
      return true;
    } catch (err) {
      const message = formatErrorMessage(err);
      this.opts.logger.warn(
        `TunnelProxy: RPC WS send failed for ${context}: ${message}`,
      );
      this.failGatewayPendingEntry(
        entry,
        "GATEWAY_RPC_SEND_FAILED",
        `local gateway RPC send failed: ${message}`,
      );
      this.failGatewayInflight(
        "GATEWAY_RPC_SEND_FAILED",
        "local gateway RPC connection closed after send failure",
      );
      if (this.gatewayWs === ws) {
        this.gatewayWs = null;
        this.gatewayWsReady = false;
        this.gatewayWsConnecting = false;
      }
      try {
        if (typeof (ws as any).terminate === "function") {
          (ws as any).terminate();
        } else {
          ws.close();
        }
      } catch {
        // ignore
      }
      return false;
    }
  }

  // ─── Gateway connect auth helpers ───

  private resolveGatewayConnectAuth():
    | { token?: string; password?: string }
    | undefined {
    const token = this.opts.gatewayToken?.trim() || undefined;
    const password = this.opts.gatewayPassword?.trim() || undefined;
    if (!token && !password) return undefined;
    return { token, password };
  }

  private loadStoredDeviceToken(role: string): string | undefined {
    return (
      loadDeviceAuthToken({
        stateDir: this.stateDir,
        deviceId: this.deviceIdentity.deviceId,
        role,
      })?.token ?? undefined
    );
  }

  private buildGatewayConnectAuth(role: string): {
    auth?: { token?: string; deviceToken?: string; password?: string };
    authToken?: string;
    authPassword?: string;
    deviceToken?: string;
  } {
    const explicitAuth = this.resolveGatewayConnectAuth();
    const authPassword = explicitAuth?.password?.trim() || undefined;
    const explicitGatewayToken = explicitAuth?.token?.trim() || undefined;
    const deviceToken = explicitGatewayToken
      ? undefined
      : this.loadStoredDeviceToken(role);
    const authToken = explicitGatewayToken ?? deviceToken;
    const auth =
      authToken || authPassword || deviceToken
        ? { token: authToken, deviceToken, password: authPassword }
        : undefined;
    return { auth, authToken, authPassword, deviceToken };
  }

  private storeIssuedDeviceToken(params: {
    fallbackRole: string;
    fallbackScopes: string[];
    authInfo: any;
  }): void {
    const token = params.authInfo?.deviceToken;
    if (typeof token !== "string" || !token.trim()) return;
    const role =
      typeof params.authInfo?.role === "string" && params.authInfo.role.trim()
        ? params.authInfo.role.trim()
        : params.fallbackRole;
    const scopes = Array.isArray(params.authInfo?.scopes)
      ? params.authInfo.scopes.filter(
          (scope: unknown): scope is string =>
            typeof scope === "string" && !!scope.trim(),
        )
      : params.fallbackScopes;
    storeDeviceAuthToken({
      stateDir: this.stateDir,
      deviceId: this.deviceIdentity.deviceId,
      role,
      token: token.trim(),
      scopes,
    });
  }

  private maybeClearStoredDeviceTokenOnMismatch(code: number, reason: string): void {
    const explicitAuth = this.resolveGatewayConnectAuth();
    if (explicitAuth?.token || explicitAuth?.password) return;
    if (code !== 1008 || !reason.toLowerCase().includes("device token mismatch")) return;
    clearDeviceAuthToken({
      stateDir: this.stateDir,
      deviceId: this.deviceIdentity.deviceId,
      role: "operator",
    });
    this.opts.logger.warn(
      `TunnelProxy: cleared stale stored device token after gateway mismatch (deviceId=${this.deviceIdentity.deviceId})`,
    );
  }

  private async maybeAutoApproveGatewayPairing(frame: any): Promise<boolean> {
    const errorCode =
      typeof frame?.error?.code === "string" ? frame.error.code : undefined;
    const detailsCode =
      typeof frame?.error?.details?.code === "string"
        ? frame.error.details.code
        : undefined;
    if (errorCode !== "NOT_PAIRED" && detailsCode !== "PAIRING_REQUIRED") {
      return false;
    }

    const requestId =
      typeof frame?.error?.details?.requestId === "string"
        ? frame.error.details.requestId.trim()
        : "";
    const reason =
      typeof frame?.error?.details?.reason === "string"
        ? frame.error.details.reason.trim()
        : "";

    if (!requestId) {
      this.opts.logger.warn(
        "TunnelProxy: gateway pairing required but requestId missing; unable to auto-approve",
      );
      return false;
    }

    if (
      reason &&
      reason !== "not-paired" &&
      reason !== "scope-upgrade"
    ) {
      this.opts.logger.warn(
        `TunnelProxy: gateway pairing required with unsupported reason=${reason}; skipping auto-approve`,
      );
      return false;
    }

    if (this.gatewayWsAutoPairingApprovals >= MAX_AUTO_PAIRING_APPROVALS) {
      this.opts.logger.warn(
        `TunnelProxy: reached auto-pairing retry limit (${MAX_AUTO_PAIRING_APPROVALS}); requestId=${requestId}`,
      );
      return false;
    }

    this.gatewayWsPairingApprovalPending = true;
    try {
      const approveDevicePairing = await loadApproveDevicePairing(this.opts.logger);
      if (!approveDevicePairing) {
        this.gatewayWsPairingApprovalPending = false;
        return false;
      }
      const approved = await approveDevicePairing(requestId, this.hostStateDir);
      if (!approved) {
        this.gatewayWsPairingApprovalPending = false;
        this.opts.logger.warn(
          `TunnelProxy: gateway pairing request ${requestId} not found in host state ${this.hostStateDir}`,
        );
        return false;
      }
      this.gatewayWsAutoPairingApprovals += 1;
      this.gatewayWsReconnectRequested = true;
      this.gatewayWsPairingApprovalPending = false;
      this.opts.logger.info(
        `TunnelProxy: auto-approved local gateway pairing request ${requestId} (reason=${reason || "not-paired"}, hostStateDir=${this.hostStateDir}, approval=${this.gatewayWsAutoPairingApprovals}/${MAX_AUTO_PAIRING_APPROVALS})`,
      );
      return true;
    } catch (err: any) {
      this.gatewayWsPairingApprovalPending = false;
      this.opts.logger.warn(
        `TunnelProxy: failed to auto-approve gateway pairing request ${requestId}: ${err?.message ?? String(err)}`,
      );
      return false;
    }
  }

  // ─── Gateway RPC WebSocket lifecycle ───

  private normalizeSessionKey(value: unknown): string | undefined {
    return typeof value === "string" && value.trim() ? value.trim() : undefined;
  }

  private normalizeRunId(value: unknown): string | undefined {
    return typeof value === "string" && value.trim() ? value.trim() : undefined;
  }

  private pruneRecentAbortedChatRuns(now = Date.now()): void {
    for (const [runId, entry] of this.recentAbortedChatRuns) {
      if (now - entry.abortedAtMs > RECENT_ABORTED_CHAT_RUN_TTL_MS) {
        this.recentAbortedChatRuns.delete(runId);
      }
    }
  }

  private rememberRecentAbortedChatRun(runId: unknown, sessionKey: unknown): void {
    const normalizedRunId = this.normalizeRunId(runId);
    const normalizedSessionKey = this.normalizeSessionKey(sessionKey);
    if (!normalizedRunId || !normalizedSessionKey) return;
    this.pruneRecentAbortedChatRuns();
    this.recentAbortedChatRuns.set(normalizedRunId, {
      sessionKey: normalizedSessionKey,
      abortedAtMs: Date.now(),
    });
  }

  private getAssistantMessageText(message: any): string {
    if (!message || typeof message !== "object") return "";
    if (typeof message.text === "string") return message.text;
    if (!Array.isArray(message.content)) return "";
    return message.content
      .filter(
        (item: any) =>
          item &&
          typeof item === "object" &&
          item.type === "text" &&
          typeof item.text === "string",
      )
      .map((item: any) => item.text)
      .join("");
  }

  private looksLikeSyntheticAbortFailureText(text: string): boolean {
    const normalized = text.trim().toLowerCase();
    if (!normalized) return false;
    return (
      normalized.includes("agent failed before reply:") &&
      normalized.includes("aborted") &&
      normalized.includes("openclaw logs --follow")
    );
  }

  private isSyntheticAbortHistoryMessage(
    message: any,
    sessionKey: string,
  ): boolean {
    const text = this.getAssistantMessageText(message);
    if (!this.looksLikeSyntheticAbortFailureText(text)) return false;
    const messageTimestamp =
      typeof message?.timestamp === "number" ? message.timestamp : null;
    const now = Date.now();
    this.pruneRecentAbortedChatRuns(now);
    for (const entry of this.recentAbortedChatRuns.values()) {
      if (entry.sessionKey !== sessionKey) continue;
      if (messageTimestamp === null) return true;
      if (Math.abs(messageTimestamp - entry.abortedAtMs) <= RECENT_ABORTED_CHAT_RUN_TTL_MS) {
        return true;
      }
    }
    return false;
  }

  private maybeRewriteGatewayFrame(text: string, frame: any): string | null {
    this.pruneRecentAbortedChatRuns();

    if (
      frame?.type === "event" &&
      frame?.event === "chat" &&
      frame?.payload?.state === "aborted"
    ) {
      this.rememberRecentAbortedChatRun(
        frame.payload?.runId,
        frame.payload?.sessionKey,
      );
      return text;
    }

    if (frame?.type === "ack" && frame?.ok === false && typeof frame?.id === "string") {
      this.gatewayReqMetaById.delete(frame.id);
      return text;
    }

    let reqMeta: PendingGatewayReqMeta | undefined;
    if (frame?.type === "res" && typeof frame?.id === "string") {
      reqMeta = this.gatewayReqMetaById.get(frame.id);
      this.gatewayReqMetaById.delete(frame.id);
    }

    if (
      frame?.type === "res" &&
      frame?.ok === true &&
      reqMeta?.method === "chat.abort"
    ) {
      const runIds = Array.isArray(frame?.payload?.runIds)
        ? frame.payload.runIds
        : [];
      for (const runId of runIds) {
        this.rememberRecentAbortedChatRun(runId, reqMeta.sessionKey);
      }
      return text;
    }

    if (
      frame?.type === "event" &&
      frame?.event === "chat" &&
      frame?.payload?.state === "final"
    ) {
      const runId = this.normalizeRunId(frame?.payload?.runId);
      const assistantText = this.getAssistantMessageText(frame?.payload?.message);
      const recentAbort = runId ? this.recentAbortedChatRuns.get(runId) : undefined;
      if (
        recentAbort &&
        this.looksLikeSyntheticAbortFailureText(assistantText)
      ) {
        this.opts.logger.info(
          `TunnelProxy: suppressing synthetic abort failure final for runId=${runId}`,
        );
        return null;
      }
      return text;
    }

    if (
      frame?.type === "res" &&
      frame?.ok === true &&
      reqMeta?.method === "chat.history" &&
      Array.isArray(frame?.payload?.messages)
    ) {
      const sessionKey =
        this.normalizeSessionKey(frame?.payload?.sessionKey) ?? reqMeta.sessionKey;
      if (!sessionKey) return text;
      const filteredMessages = frame.payload.messages.filter(
        (message: any) =>
          !this.isSyntheticAbortHistoryMessage(message, sessionKey),
      );
      if (filteredMessages.length === frame.payload.messages.length) {
        return text;
      }
      this.opts.logger.info(
        `TunnelProxy: removed ${frame.payload.messages.length - filteredMessages.length} synthetic abort history message(s) for session=${sessionKey}`,
      );
      return JSON.stringify({
        ...frame,
        payload: {
          ...frame.payload,
          messages: filteredMessages,
        },
      });
    }

    return text;
  }

  /** 确保到本地 Gateway 的 RPC WebSocket 已连接且握手完成 */
  private ensureGatewayWs(): void {
    if (this.gatewayWsReady && this.gatewayWs?.readyState === WebSocket.OPEN)
      return;
    if (this.gatewayWsConnecting) return;

    this.gatewayWsConnecting = true;
    this.gatewayWsReady = false;
    const wsUrl = this.opts.gatewayBaseUrl.replace(/^http/, "ws");

    this.opts.logger.info(
      `TunnelProxy: RPC WS connecting to gateway ${wsUrl} (pending=${this.gatewayWsPending.length})`,
    );

    let ws: WebSocket;
    try {
      ws = new WebSocket(wsUrl, {
        handshakeTimeout: GATEWAY_RPC_HANDSHAKE_TIMEOUT_MS,
      });
    } catch (err) {
      const message = formatErrorMessage(err);
      this.gatewayWsConnecting = false;
      this.opts.logger.error(
        `TunnelProxy: RPC WS failed to create gateway connection: ${message}`,
      );
      this.failAllGatewayRequests(
        "GATEWAY_RPC_CONNECT_FAILED",
        `local gateway RPC connect failed: ${message}`,
      );
      return;
    }

    this.gatewayWs = ws;
    let handshakeTimer: ReturnType<typeof setTimeout> | null = setTimeout(
      () => {
        handshakeTimer = null;
        if (this.gatewayWs !== ws || this.gatewayWsReady) return;
        this.opts.logger.warn(
          `TunnelProxy: RPC WS handshake timed out after ${GATEWAY_RPC_HANDSHAKE_TIMEOUT_MS}ms (pending=${this.gatewayWsPending.length})`,
        );
        this.gatewayWs = null;
        this.gatewayWsReady = false;
        this.gatewayWsConnecting = false;
        this.failAllGatewayRequests(
          "GATEWAY_RPC_HANDSHAKE_TIMEOUT",
          "local gateway RPC handshake timed out",
        );
        try {
          if (typeof (ws as any).terminate === "function") {
            (ws as any).terminate();
          } else {
            ws.close();
          }
        } catch {
          // ignore
        }
      },
      GATEWAY_RPC_HANDSHAKE_TIMEOUT_MS,
    );

    const clearHandshakeTimer = () => {
      if (handshakeTimer) {
        clearTimeout(handshakeTimer);
        handshakeTimer = null;
      }
    };

    ws.on("open", () => {
      this.opts.logger.info(
        `TunnelProxy: RPC WS tcp connected, waiting for connect.challenge (pending=${this.gatewayWsPending.length})`,
      );
    });

    ws.on("message", async (data: WebSocket.RawData) => {
      let text: string;
      if (typeof data === "string") {
        text = data;
      } else if (Buffer.isBuffer(data)) {
        text = data.toString();
      } else {
        text = Buffer.from(data as ArrayBuffer).toString();
      }

      const preview = text.length <= 500 ? text : text.substring(0, 500) + "…";
      this.opts.logger.info(
        `TunnelProxy: RPC WS ← gateway (${text.length} chars): ${preview}`,
      );

      let frame: any;
      try {
        frame = JSON.parse(text);
      } catch {
        this.opts.logger.warn(
          "TunnelProxy: RPC WS received non-JSON, ignoring",
        );
        return;
      }

      // 1) 收到 connect.challenge → 回复 connect 请求（带设备身份签名）
      if (frame.type === "event" && frame.event === "connect.challenge") {
        this.handleConnectChallenge(ws, frame);
        return;
      }

      // 2) 收到 connect 的 hello-ok 响应 → 握手完成，flush 待发帧
      if (
        frame.type === "res" &&
        frame.ok === true &&
        frame.payload?.type === "hello-ok"
      ) {
        clearHandshakeTimer();
        this.storeIssuedDeviceToken({
          fallbackRole: "operator",
          fallbackScopes: [...GATEWAY_OPERATOR_SCOPES],
          authInfo: frame.payload?.auth,
        });
        this.gatewayWsAutoPairingApprovals = 0;
        this.gatewayWsReconnectRequested = false;
        this.gatewayWsReady = true;
        this.gatewayWsConnecting = false;
        this.opts.logger.info(
          `TunnelProxy: RPC WS handshake done (hello-ok), flushing ${this.gatewayWsPending.length} pending frames`,
        );
        const pending = this.gatewayWsPending;
        this.gatewayWsPending = [];
        for (let i = 0; i < pending.length; i++) {
          const entry = pending[i];
          if (!this.sendGatewayPayload(ws, entry, entry.reqId ?? "pending frame")) {
            return;
          }
        }
        return;
      }

      // 3) 收到 connect 失败响应 → 关闭连接
      if (frame.type === "res" && frame.ok === false && !this.gatewayWsReady) {
        const autoApproved = await this.maybeAutoApproveGatewayPairing(frame);
        if (autoApproved) {
          const wsAlreadyClosed =
            ws.readyState === WebSocket.CLOSING ||
            ws.readyState === WebSocket.CLOSED ||
            this.gatewayWs !== ws;
          this.opts.logger.info(
            `TunnelProxy: RPC WS handshake requested local pairing approval, reconnecting${wsAlreadyClosed ? " immediately" : " after close"} (pending=${this.gatewayWsPending.length})`,
          );
          if (wsAlreadyClosed) {
            this.gatewayWsReconnectRequested = false;
            this.ensureGatewayWs();
            return;
          }
          ws.close(1000, "pairing approved, reconnecting");
          return;
        }
        this.opts.logger.error(
          `TunnelProxy: RPC WS handshake failed (pending=${this.gatewayWsPending.length}): ${previewText(JSON.stringify(frame.error), 500)}`,
        );
        clearHandshakeTimer();
        this.failAllGatewayRequests(
          "GATEWAY_RPC_HANDSHAKE_FAILED",
          `local gateway RPC handshake failed: ${previewText(JSON.stringify(frame.error), 500)}`,
        );
        ws.close();
        return;
      }

      // 4) 握手后的普通消息 → 透传回 Relay
      const rewritten = this.maybeRewriteGatewayFrame(text, frame);
      if (rewritten !== null) {
        this.opts.client.sendRaw(rewritten);
      }
    });

    ws.on("close", (code: number, reason: Buffer) => {
      clearHandshakeTimer();
      const wasReady = this.gatewayWsReady;
      const pendingCount = this.gatewayWsPending.length;
      const reasonText = reason.toString();
      const shouldReconnect =
        this.gatewayWsReconnectRequested && pendingCount > 0;
      const shouldHoldForPairing =
        this.gatewayWsPairingApprovalPending && pendingCount > 0;
      this.opts.logger.info(
        `TunnelProxy: RPC WS closed by gateway (code=${code}, reason=${reasonText}, ready=${wasReady}, pending=${pendingCount}, activeWs=${this.wsProxy.activeCount})`,
      );
      if (this.gatewayWs === ws) {
        this.gatewayWs = null;
        this.gatewayWsReady = false;
      }
      this.gatewayWsConnecting = false;
      this.maybeClearStoredDeviceTokenOnMismatch(code, reasonText);
      if (shouldReconnect) {
        this.gatewayWsReconnectRequested = false;
        this.opts.logger.info(
          `TunnelProxy: retrying RPC WS after local pairing approval (pending=${this.gatewayWsPending.length})`,
        );
        queueMicrotask(() => this.ensureGatewayWs());
      } else if (
        !shouldHoldForPairing &&
        (pendingCount > 0 || this.gatewayReqMetaById.size > 0)
      ) {
        this.failAllGatewayRequests(
          "GATEWAY_RPC_DISCONNECTED",
          `local gateway RPC disconnected: code=${code}, reason=${reasonText}`,
        );
      }
    });

    ws.on("error", (err: Error) => {
      clearHandshakeTimer();
      this.opts.logger.warn(
        `TunnelProxy: RPC WS error: ${err.message} (ready=${this.gatewayWsReady}, pending=${this.gatewayWsPending.length}, activeWs=${this.wsProxy.activeCount})`,
      );
      const shouldFailRequests =
        this.gatewayWs === ws &&
        (this.gatewayWsPending.length > 0 || this.gatewayReqMetaById.size > 0);
      this.gatewayWsConnecting = false;
      if (this.gatewayWs === ws) {
        this.gatewayWs = null;
        this.gatewayWsReady = false;
      }
      if (shouldFailRequests) {
        this.failAllGatewayRequests(
          "GATEWAY_RPC_ERROR",
          `local gateway RPC error: ${err.message}`,
        );
      }
      try {
        if (typeof (ws as any).terminate === "function") {
          (ws as any).terminate();
        }
      } catch {
        // ignore
      }
    });
  }

  private handleConnectChallenge(ws: WebSocket, frame: any): void {
    const challengeNonce: string = frame.payload?.nonce ?? "";
    const connectRequestId = `tunnel-connect-${randomUUID()}`;
    const role = "operator";
    const scopes = [...GATEWAY_OPERATOR_SCOPES];
    const gatewayConnectAuth = this.buildGatewayConnectAuth(role);
    this.opts.logger.info(
      `TunnelProxy: received connect.challenge (nonce=${challengeNonce}, connectReqId=${connectRequestId}, hasToken=${!!gatewayConnectAuth.authToken}, hasPassword=${!!gatewayConnectAuth.authPassword}, hasDeviceToken=${!!gatewayConnectAuth.deviceToken}), sending connect request with device identity`,
    );

    const signedAtMs = Date.now();
    const clientId = "gateway-client";
    const clientMode = "backend";

    const authPayload = buildDeviceAuthPayload({
      deviceId: this.deviceIdentity.deviceId,
      clientId,
      clientMode,
      role,
      scopes,
      signedAtMs,
      token: gatewayConnectAuth.authToken ?? null,
      nonce: challengeNonce,
    });
    const signature = signDevicePayload(
      this.deviceIdentity.privateKeyPem,
      authPayload,
    );

    const connectReq = {
      type: "req",
      id: connectRequestId,
      method: "connect",
      params: {
        minProtocol: GATEWAY_RPC_MIN_PROTOCOL,
        maxProtocol: GATEWAY_RPC_MAX_PROTOCOL,
        client: {
          id: clientId,
          version: "1.0.0",
          platform: process.platform,
          mode: clientMode,
          instanceId: RELAY_TUNNEL_GATEWAY_CLIENT_INSTANCE_ID,
        },
        role,
        scopes,
        ...(gatewayConnectAuth.auth ? { auth: gatewayConnectAuth.auth } : {}),
        device: {
          id: this.deviceIdentity.deviceId,
          publicKey: publicKeyRawBase64UrlFromPem(
            this.deviceIdentity.publicKeyPem,
          ),
          signature,
          signedAt: signedAtMs,
          nonce: challengeNonce,
        },
      },
    };
    ws.send(JSON.stringify(connectReq));
  }
}

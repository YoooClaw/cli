/**
 * RelayDispatcher —— CLI daemon 模式下的 Relay 帧分发器。
 *
 * 为什么不直接复用 phone-notifications/tunnel/proxy 的 TunnelProxy：
 *   TunnelProxy 把每个 `req` 帧反代到「本地 OpenClaw 桌面端 gateway WebSocket」，依赖 OpenClaw 自带的
 *   `connect.challenge` / `pairing` 协议；daemon 不实现那套，反代会 401 卡死。
 *
 * 本 dispatcher 的取舍：保留与插件**对外完全一致**的 Relay 协议（RelayClient 复用，frame schema 不变），
 * 但**绕过 OpenClaw gateway WS 这一层耦合**，直接把 `req` 帧同进程交给 `runtime.callGateway` 处理。
 * 手机端 / Relay 服务端对此完全无感知。
 *
 * 当前覆盖：
 *   - `type: "req"`  → callGateway → 回 `{type:"res", id, ok, payload?, error?}`
 *   - `type: "request" / ws_*` → 暂不支持，回 proxy_error 或日志 warn（手机端目前只发 req）
 *
 * `pushEvent(event, payload)` —— daemon 主动反向推事件给手机端（recording.status 等）。
 */
import type { DaemonLogger } from "./logger.js";
import type { StandaloneRuntime } from "./runtime.js";
import {
  RELAY_INTERNAL_HTTP_HEADER,
  mapRelayHttpPath,
  type RelayClient,
} from "../shared.js";

export const RELAY_INTERNAL_CLIENT_LABEL_HEADER = "x-yoooclaw-internal-client-label";

interface ReqFrame {
  type: "req";
  id: string;
  method: string;
  params?: Record<string, unknown>;
}

interface RequestFrame {
  type: "request";
  id: string;
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: string;
}

interface AnyFrame {
  type: string;
  id?: string;
  [k: string]: unknown;
}

export interface RelayDispatcherDeps {
  client: RelayClient;
  runtime: StandaloneRuntime;
  logger: DaemonLogger;
  /**
   * 用于把 RequestFrame（HTTP-style，如 POST /notifications）loopback 到 daemon 自己的 HTTP server。
   * 这样能直接复用现成的路由实现（包括 authorized check、JSON 解析、ingest 逻辑），
   * 不必为每条 frame 重新 mock node:http 的 IncomingMessage/ServerResponse。
   */
  httpBaseUrl: string;
  /** loopback 时带的 gateway token；未设则不加 Authorization（daemon 默认放行无 token 模式）。 */
  httpToken?: string;
  /** 当前 Relay 隧道对应的 api-key label。 */
  clientLabel: string;
}

export class RelayDispatcher {
  constructor(private readonly deps: RelayDispatcherDeps) {}

  /** 挂上 RelayClient 的 inbound 回调；幂等：重复调只会重新绑定（沿用 onInbound 的覆盖语义）。 */
  start(): void {
    this.deps.client.onInbound((frame) => {
      void this.handleFrame(frame as unknown as AnyFrame);
    });
  }

  private async handleFrame(frame: AnyFrame): Promise<void> {
    const { logger } = this.deps;
    switch (frame.type) {
      case "req":
        await this.handleReq(frame as unknown as ReqFrame);
        return;
      case "request":
        await this.handleRequest(frame as unknown as RequestFrame);
        return;
      case "ws_open":
      case "ws_data":
      case "ws_close":
        logger.warn(
          `[relay-dispatcher] 暂不支持 WS 子通道帧（type=${frame.type}, id=${frame.id ?? "?"}）`,
        );
        return;
      default:
        logger.warn(`[relay-dispatcher] 未识别帧 type=${frame.type}`);
    }
  }

  private async handleRequest(frame: RequestFrame): Promise<void> {
    const { client, logger, httpBaseUrl, httpToken } = this.deps;
    const { id, method, path, headers: srcHeaders, body } = frame;
    const mappedPath = mapRelayHttpPath(path);
    const url = httpBaseUrl + (mappedPath.startsWith("/") ? mappedPath : "/" + mappedPath);
    const headers: Record<string, string> = { ...srcHeaders };
    if (httpToken && !headers["authorization"] && !headers["Authorization"]) {
      headers["authorization"] = `Bearer ${httpToken}`;
    }
    headers[RELAY_INTERNAL_HTTP_HEADER] = "1";
    headers[RELAY_INTERNAL_CLIENT_LABEL_HEADER] = this.deps.clientLabel;

    logger.info(
      `[relay-dispatcher] request id=${id} ${method} ${path} → ${mappedPath} loopback`,
    );

    let status = 502;
    let resHeaders: Record<string, string> = {};
    let resBody = "";
    try {
      const res = await fetch(url, { method, headers, body });
      status = res.status;
      resHeaders = Object.fromEntries(res.headers.entries());
      resBody = await res.text();
    } catch (err) {
      logger.error(`[relay-dispatcher] loopback 失败 id=${id} ${method} ${path}: ${(err as Error).message}`);
      client.sendRaw(
        JSON.stringify({
          type: "proxy_error",
          id,
          status: 502,
          message: `daemon loopback 失败: ${(err as Error).message}`,
        }),
      );
      return;
    }

    client.sendRaw(
      JSON.stringify({
        type: "proxy_response",
        id,
        status,
        headers: resHeaders,
        body: resBody,
      }),
    );
  }

  private async handleReq(frame: ReqFrame): Promise<void> {
    const { client, runtime, logger } = this.deps;
    const { id, method, params } = frame;
    logger.info(`[relay-dispatcher] req id=${id} method=${method}`);
    let ok = false;
    let payload: unknown;
    let error: { code: string; message: string } | undefined;
    try {
      const result = await runtime.callGateway(method, params, {
        clientLabel: this.deps.clientLabel,
        authKind: "relay-api-key",
      });
      ok = result.ok;
      payload = result.data;
      error = result.error;
    } catch (err) {
      ok = false;
      error = { code: "INTERNAL_ERROR", message: (err as Error).message };
      logger.error(`[relay-dispatcher] callGateway 异常: ${(err as Error).message}`);
    }
    // JSON.stringify 会跳过 value 为 undefined 的键，所以无需手动剔除
    client.sendRaw(JSON.stringify({ type: "res", id, ok, payload, error }));
  }

  /** daemon → 手机端反向推 Gateway event。 */
  pushEvent(event: string, payload: unknown): void {
    this.deps.client.sendRaw(JSON.stringify({ type: "event", event, payload }));
  }
}

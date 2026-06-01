import WebSocket from "ws";
import type { RelayClient } from "./relay-client.js";
import type { WsOpenFrame, WsDataFrame, WsCloseFrame } from "./types.js";
import { mapPath } from "./http-proxy.js";

const GATEWAY_WS_HANDSHAKE_TIMEOUT_MS = 10_000;

export interface WsProxyOptions {
  gatewayBaseUrl: string;
  client: RelayClient;
  logger: {
    info: (msg: string) => void;
    warn: (msg: string) => void;
    error: (msg: string) => void;
  };
}

export class WsProxy {
  /** 活跃的 WebSocket 代理连接，按帧 id 索引 */
  private connections = new Map<string, WebSocket>();

  constructor(private readonly opts: WsProxyOptions) {}

  get activeCount(): number {
    return this.connections.size;
  }

  cleanup(): void {
    const connections = [...this.connections.entries()];
    this.connections.clear();
    for (const [id, ws] of connections) {
      this.opts.logger.info(`WsProxy: closing WS id=${id}`);
      try {
        ws.close();
      } catch {
        // ignore
      }
    }
  }

  handleWsOpen(frame: WsOpenFrame): void {
    const wsUrl =
      this.opts.gatewayBaseUrl.replace(/^http/, "ws") +
      mapPath(frame.path);
    this.opts.logger.info(
      `TunnelProxy: WS open id=${frame.id}, path=${frame.path} → ${wsUrl}`,
    );

    try {
      const existing = this.connections.get(frame.id);
      if (existing) {
        this.opts.logger.warn(
          `TunnelProxy: WS id=${frame.id} already exists, closing previous gateway connection`,
        );
        try {
          existing.close(1000, "replaced by new relay ws_open");
        } catch {
          // ignore
        }
      }

      const ws = new WebSocket(wsUrl, {
        headers: frame.headers,
        handshakeTimeout: GATEWAY_WS_HANDSHAKE_TIMEOUT_MS,
      });
      this.connections.set(frame.id, ws);

      let notifiedClosed = false;
      let connectTimer: ReturnType<typeof setTimeout> | null = setTimeout(
        () => {
          connectTimer = null;
          if (
            this.connections.get(frame.id) !== ws ||
            ws.readyState === WebSocket.OPEN
          ) {
            return;
          }
          this.opts.logger.warn(
            `TunnelProxy: WS id=${frame.id} connect timed out after ${GATEWAY_WS_HANDSHAKE_TIMEOUT_MS}ms`,
          );
          notifyClosed(1011, "gateway websocket connect timeout");
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
        GATEWAY_WS_HANDSHAKE_TIMEOUT_MS,
      );

      const clearConnectTimer = () => {
        if (connectTimer) {
          clearTimeout(connectTimer);
          connectTimer = null;
        }
      };

      const notifyClosed = (code: number, reason: string) => {
        if (notifiedClosed) return;
        notifiedClosed = true;
        clearConnectTimer();
        if (this.connections.get(frame.id) === ws) {
          this.connections.delete(frame.id);
        }
        this.opts.client.send({
          type: "ws_close",
          id: frame.id,
          code,
          reason,
        });
      };

      ws.on("open", () => {
        clearConnectTimer();
        if (this.connections.get(frame.id) !== ws) {
          try {
            ws.close(1000, "stale relay ws_open");
          } catch {
            // ignore
          }
          return;
        }
        this.opts.logger.info(
          `TunnelProxy: WS id=${frame.id} connected to gateway, active=${this.connections.size}`,
        );
        this.opts.client.send({
          type: "ws_opened",
          id: frame.id,
        });
      });

      ws.on("message", (data: WebSocket.RawData) => {
        if (this.connections.get(frame.id) !== ws) {
          return;
        }
        const text =
          typeof data === "string"
            ? data
            : Buffer.isBuffer(data)
              ? data.toString()
              : String(data);

        this.opts.logger.info(
          `TunnelProxy: WS id=${frame.id} ← gateway data (${text.length} chars): ${text.substring(0, 200)}`,
        );
        this.opts.client.send({
          type: "ws_data",
          id: frame.id,
          data: text,
        });
      });

      ws.on("close", (code: number, reason: Buffer) => {
        clearConnectTimer();
        const reasonText = reason.toString();
        const isCurrent = this.connections.get(frame.id) === ws;
        if (isCurrent) {
          this.connections.delete(frame.id);
        }
        this.opts.logger.info(
          `TunnelProxy: WS id=${frame.id} closed by gateway (code=${code}, reason=${reasonText}), active=${this.connections.size}`,
        );
        if (isCurrent) {
          notifyClosed(code, reasonText);
        }
      });

      ws.on("error", (err: Error) => {
        clearConnectTimer();
        this.opts.logger.warn(
          `TunnelProxy: WS id=${frame.id} error: ${err.message}, active=${this.connections.size}`,
        );
        if (this.connections.get(frame.id) === ws) {
          notifyClosed(1011, err.message);
        }
        try {
          if (typeof (ws as any).terminate === "function") {
            (ws as any).terminate();
          }
        } catch {
          // ignore
        }
      });
    } catch (err) {
      this.opts.logger.error(
        `TunnelProxy: WS id=${frame.id} failed to connect: ${err instanceof Error ? err.message : String(err)}`,
      );
      this.opts.client.send({
        type: "ws_close",
        id: frame.id,
        code: 1011,
        reason: `failed to connect: ${err instanceof Error ? err.message : String(err)}`,
      });
    }
  }

  handleWsData(frame: WsDataFrame): void {
    const ws = this.connections.get(frame.id);
    if (ws?.readyState === WebSocket.OPEN) {
      this.opts.logger.info(
        `TunnelProxy: WS id=${frame.id} → gateway data (${frame.data.length} chars): ${frame.data.substring(0, 200)}`,
      );
      ws.send(frame.data);
    } else {
      this.opts.logger.warn(
        `TunnelProxy: WS id=${frame.id} data dropped, ws not open (exists=${!!ws}, readyState=${ws?.readyState ?? "N/A"})`,
      );
    }
  }

  handleWsClose(frame: WsCloseFrame): void {
    const ws = this.connections.get(frame.id);
    if (ws) {
      this.opts.logger.info(
        `TunnelProxy: WS id=${frame.id} close requested by relay (code=${frame.code ?? 1000}, reason=${frame.reason ?? ""})`,
      );
      this.connections.delete(frame.id);
      try {
        ws.close(frame.code ?? 1000, frame.reason ?? "");
      } catch {
        // ignore
      }
    } else {
      this.opts.logger.warn(
        `TunnelProxy: WS id=${frame.id} close requested but connection not found`,
      );
    }
  }
}

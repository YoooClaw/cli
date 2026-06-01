/** Relay Tunnel 运行时可调参数（来自 plugins.entries.phone-notifications.config.relay） */
export interface RelayTunnelConfig {
  /** 心跳间隔（秒），默认 10 */
  heartbeatSec?: number;
  /** 重连基础退避时间（毫秒），默认 2000 */
  reconnectBackoffMs?: number;
}

// ─── Relay ↔ 插件 隧道协议帧 ───

/** Relay → 插件：HTTP 请求帧 */
export interface RequestFrame {
  type: "request";
  /** 请求唯一 ID，用于匹配响应 */
  id: string;
  method: string;
  path: string;
  headers: Record<string, string>;
  body?: string;
}

/** 插件 → Relay：HTTP 响应帧 */
export interface ResponseFrame {
  type: "proxy_response";
  id: string;
  status: number;
  headers: Record<string, string>;
  body?: string;
}

/** 插件 → Relay：流式响应片段帧 */
export interface StreamFrame {
  type: "stream";
  id: string;
  state: "delta" | "end";
  data: string;
}

/** 插件 → Relay：错误帧 */
export interface ErrorFrame {
  type: "proxy_error";
  id: string;
  status: number;
  message: string;
}

/** Relay → 插件：WebSocket 打开请求 */
export interface WsOpenFrame {
  type: "ws_open";
  id: string;
  path: string;
  headers: Record<string, string>;
}

/** 插件 → Relay：WebSocket 已打开确认 */
export interface WsOpenedFrame {
  type: "ws_opened";
  id: string;
}

/** 双向：WebSocket 数据帧 */
export interface WsDataFrame {
  type: "ws_data";
  id: string;
  data: string;
}

/** 双向：WebSocket 关闭帧 */
export interface WsCloseFrame {
  type: "ws_close";
  id: string;
  code?: number;
  reason?: string;
}

/** Relay → 插件：Gateway RPC 请求帧（原样转发到 Gateway WebSocket） */
export interface ReqFrame {
  type: "req";
  id: string;
  method: string;
  params: Record<string, unknown>;
}

/** Relay → 插件：所有可能的入站帧 */
export type InboundFrame = RequestFrame | ReqFrame | WsOpenFrame | WsDataFrame | WsCloseFrame;

/** 心跳使用原始文本 ping/pong，不进入 JSON 帧协议。 */

/** 插件 → Relay：所有可能的出站帧 */
export type OutboundFrame =
  | ResponseFrame
  | StreamFrame
  | ErrorFrame
  | WsOpenedFrame
  | WsDataFrame
  | WsCloseFrame;

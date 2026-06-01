/**
 * daemon ↔ CLI 客户端的内部 RPC（localhost HTTP）。
 *
 * 鉴权：从 config.auth.tokenRef 解析 gateway token，加 `Authorization: Bearer`。
 * daemon 未运行（连接被拒）时统一抛 YOOOCLAW_DAEMON_NOT_RUNNING。
 */
import type { ProfilePaths } from "../paths.js";
import { YoooclawError } from "../errors.js";
import { loadConfig } from "../config/store.js";
import { resolveGatewayToken } from "../credentials/store.js";
import { daemonState } from "./lock.js";

export interface DaemonClientOptions {
  /** 覆盖 base URL（默认按 lock / config 推导）。 */
  baseUrl?: string;
  timeoutMs?: number;
}

export class DaemonClient {
  readonly baseUrl: string;
  private readonly token?: string;
  private readonly timeoutMs: number;

  constructor(
    private readonly paths: ProfilePaths,
    opts: DaemonClientOptions = {},
  ) {
    const config = loadConfig(paths);
    const state = daemonState(paths);
    const bind = state.lock?.bind ?? config.daemon.bind;
    // 0.0.0.0 监听时，本机用 127.0.0.1 连接
    const host = bind === "0.0.0.0" ? "127.0.0.1" : bind;
    const port = state.lock?.port ?? config.daemon.port;
    this.baseUrl = opts.baseUrl ?? `http://${host}:${port}`;
    this.token = resolveGatewayToken(config).value;
    this.timeoutMs = opts.timeoutMs ?? 10_000;
  }

  async request<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
    extraHeaders?: Record<string, string>,
  ): Promise<{ status: number; body: T }> {
    const headers: Record<string, string> = { ...extraHeaders };
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    let payload: string | undefined;
    if (body !== undefined) {
      headers["Content-Type"] ??= "application/json";
      payload = typeof body === "string" ? body : JSON.stringify(body);
    }

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    let res: Response;
    try {
      res = await fetch(`${this.baseUrl}${path}`, {
        method,
        headers,
        body: payload,
        signal: controller.signal,
      });
    } catch (err) {
      const code = (err as NodeJS.ErrnoException).code;
      if (code === "ECONNREFUSED" || (err as Error).name === "AbortError") {
        throw new YoooclawError(
          "YOOOCLAW_DAEMON_NOT_RUNNING",
          "daemon 未启动或无响应",
          { hint: "先执行 yoooclaw daemon start", baseUrl: this.baseUrl },
        );
      }
      throw new YoooclawError("YOOOCLAW_NETWORK_ERROR", `调用 daemon 失败：${(err as Error).message}`);
    } finally {
      clearTimeout(timer);
    }

    const text = await res.text();
    let parsed: unknown = text;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        /* 非 JSON 响应原样返回 */
      }
    }
    if (res.status === 401 || res.status === 403) {
      throw new YoooclawError("YOOOCLAW_UNAUTHORIZED", "daemon 拒绝鉴权（token 不一致）", {
        status: res.status,
      });
    }
    return { status: res.status, body: parsed as T };
  }

  get<T = unknown>(path: string): Promise<{ status: number; body: T }> {
    return this.request<T>("GET", path);
  }

  post<T = unknown>(path: string, body?: unknown): Promise<{ status: number; body: T }> {
    return this.request<T>("POST", path, body);
  }
}

/** 确保 daemon 在运行，否则抛 DAEMON_NOT_RUNNING（用于 🟡 命令前置检查）。 */
export function assertDaemonRunning(paths: ProfilePaths): void {
  const state = daemonState(paths);
  if (!state.running) {
    throw new YoooclawError(
      "YOOOCLAW_DAEMON_NOT_RUNNING",
      "daemon 未运行",
      { hint: "先执行 yoooclaw daemon start", stale: state.stale },
    );
  }
}

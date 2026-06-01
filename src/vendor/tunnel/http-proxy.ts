import type { RelayClient } from "./relay-client.js";
import type { RequestFrame } from "./types.js";
import { previewText, summarizeRequestHeaders } from "./utils.js";

export const RELAY_INTERNAL_HTTP_HEADER = "x-openclaw-relay-internal";

export interface HttpProxyOptions {
  gatewayBaseUrl: string;
  gatewayAuthMode?: "token" | "password";
  gatewayToken?: string;
  gatewayPassword?: string;
  abortSignal?: AbortSignal;
  client: RelayClient;
  logger: {
    info: (msg: string) => void;
    warn: (msg: string) => void;
    error: (msg: string) => void;
  };
}

// ─── 路径映射 ───

const PATH_MAP: Record<string, string> = {
  "/api/message/messageBridge/send": "/notifications",
};

export function mapPath(p: string): string {
  return PATH_MAP[p] ?? p;
}

// ─── Auth helpers ───

function resolveGatewayConnectAuth(opts: HttpProxyOptions):
  | { token?: string; password?: string }
  | undefined {
  const token = opts.gatewayToken?.trim() || undefined;
  const password = opts.gatewayPassword?.trim() || undefined;
  if (!token && !password) return undefined;
  return { token, password };
}

export function buildLocalGatewayAuthAttempts(
  opts: HttpProxyOptions,
  baseHeaders: Record<string, string>,
): Array<{ label: string; headers: Record<string, string> }> {
  const auth = resolveGatewayConnectAuth(opts);
  const attempts: Array<{ label: string; headers: Record<string, string> }> = [];
  const seen = new Set<string>();
  const authMode = opts.gatewayAuthMode;

  const addAttempt = (kind: "token" | "password", secret?: string): void => {
    if (!secret) return;

    const dedupeKey = `${kind}:${secret}`;
    if (seen.has(dedupeKey)) return;
    seen.add(dedupeKey);

    const headers = { ...baseHeaders };
    headers.authorization = `Bearer ${secret}`;

    if (kind === "password") {
      headers["x-openclaw-password"] = secret;
    } else {
      delete headers["x-openclaw-password"];
    }

    attempts.push({
      label: kind === "token" ? "gateway-token" : "gateway-password",
      headers,
    });
  };

  if (authMode === "password") {
    addAttempt("password", auth?.password);
    addAttempt("token", auth?.token);
  } else {
    addAttempt("token", auth?.token);
    addAttempt("password", auth?.password);
  }

  if (attempts.length === 0) {
    attempts.push({
      label: "no-auth",
      headers: { ...baseHeaders },
    });
  }

  return attempts;
}

// ─── HTTP request proxy ───

export async function handleHttpRequest(
  opts: HttpProxyOptions,
  frame: RequestFrame,
): Promise<void> {
  const mappedPath = mapPath(frame.path);
  const url = new URL(mappedPath, opts.gatewayBaseUrl);
  const startedAtMs = Date.now();

  // 代理到本地 gateway 时，替换外部鉴权头为本地 gateway token/password。
  const localHeaders: Record<string, string> = {};
  for (const [k, v] of Object.entries(frame.headers ?? {})) {
    const lower = k.toLowerCase();
    if (lower !== "authorization" && lower !== "x-openclaw-password") {
      localHeaders[k] = v;
    }
  }
  localHeaders[RELAY_INTERNAL_HTTP_HEADER] = "1";

  const authAttempts = buildLocalGatewayAuthAttempts(opts, localHeaders);
  opts.logger.info(
    `TunnelProxy: HTTP id=${frame.id} ${frame.method} ${frame.path} → ${url.toString()}${summarizeRequestHeaders(frame.headers)}, authAttempts=${authAttempts.map((a) => a.label).join(" -> ")}, body=${previewText(frame.body)}`,
  );

  try {
    for (let attemptIndex = 0; attemptIndex < authAttempts.length; attemptIndex++) {
      const attempt = authAttempts[attemptIndex];
      opts.logger.info(
        `TunnelProxy: HTTP id=${frame.id} attempt ${attemptIndex + 1}/${authAttempts.length} auth=${attempt.label}`,
      );
      const res = await fetch(url.toString(), {
        method: frame.method,
        headers: attempt.headers,
        signal: opts.abortSignal,
        body:
          frame.method !== "GET" && frame.method !== "HEAD"
            ? frame.body
            : undefined,
      });

      const hasFallback = attemptIndex < authAttempts.length - 1;
      if (res.status === 401 && hasFallback) {
        const body = await res.text();
        opts.logger.warn(
          `TunnelProxy: HTTP id=${frame.id} local gateway auth via ${attempt.label} returned 401 after ${Date.now() - startedAtMs}ms, retrying next credential${body ? `, body=${previewText(body)}` : ""}`,
        );
        continue;
      }

      await sendHttpResponse(opts, {
        frameId: frame.id,
        method: frame.method,
        path: mappedPath,
        authLabel: attempt.label,
        startedAtMs,
        res,
      });
      return;
    }
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    opts.logger.error(
      `TunnelProxy: HTTP id=${frame.id} ${frame.method} ${mappedPath} failed after ${Date.now() - startedAtMs}ms: ${message}`,
    );
    opts.client.send({
      type: "proxy_error",
      id: frame.id,
      status: 502,
      message: `gateway unreachable: ${message}`,
    });
  }
}

// ─── Response helpers ───

async function sendHttpResponse(
  opts: HttpProxyOptions,
  params: {
    frameId: string;
    method: string;
    path: string;
    authLabel: string;
    startedAtMs: number;
    res: Response;
  },
): Promise<void> {
  const { frameId, method, path, authLabel, startedAtMs, res } = params;
  const contentType = res.headers.get("content-type") ?? "";
  const isStreaming = contentType.includes("text/event-stream");
  const elapsedMs = Date.now() - startedAtMs;

  opts.logger.info(
    `TunnelProxy: HTTP id=${frameId} ${method} ${path} <= ${res.status} (${elapsedMs}ms, auth=${authLabel}, content-type=${contentType}, streaming=${isStreaming})`,
  );

  if (isStreaming && res.body) {
    await streamResponse(opts, frameId, res, startedAtMs);
    return;
  }

  const body = await res.text();
  opts.logger.info(
    `TunnelProxy: HTTP id=${frameId} response body=${previewText(body)}`,
  );
  const headers: Record<string, string> = {};
  res.headers.forEach((value, key) => {
    headers[key] = value;
  });

  opts.client.send({
    type: "proxy_response",
    id: frameId,
    status: res.status,
    headers,
    body,
  });
}

async function streamResponse(
  opts: HttpProxyOptions,
  requestId: string,
  res: Response,
  startedAtMs: number,
): Promise<void> {
  const reader = res.body!.getReader();
  const decoder = new TextDecoder();
  let chunkCount = 0;

  opts.logger.info(`TunnelProxy: stream start id=${requestId}`);

  try {
    while (true) {
      if (opts.abortSignal?.aborted) {
        throw new DOMException("relay tunnel disconnected", "AbortError");
      }
      const { done, value } = await reader.read();
      if (done) break;

      chunkCount++;
      const chunk = decoder.decode(value, { stream: true });
      opts.logger.info(
        `TunnelProxy: stream delta id=${requestId} chunk#${chunkCount} (${chunk.length} chars)`,
      );
      opts.client.send({
        type: "stream",
        id: requestId,
        state: "delta",
        data: chunk,
      });
    }

    opts.logger.info(
      `TunnelProxy: stream end id=${requestId}, total chunks=${chunkCount}, totalElapsedMs=${Date.now() - startedAtMs}`,
    );
    opts.client.send({
      type: "stream",
      id: requestId,
      state: "end",
      data: "",
    });
  } catch (err) {
    opts.logger.error(
      `TunnelProxy: stream error id=${requestId} after ${chunkCount} chunks and ${Date.now() - startedAtMs}ms: ${err instanceof Error ? err.message : String(err)}`,
    );
    opts.client.send({
      type: "proxy_error",
      id: requestId,
      status: 502,
      message: `stream error: ${err instanceof Error ? err.message : String(err)}`,
    });
  }
}

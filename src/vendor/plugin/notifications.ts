import { randomBytes } from "node:crypto";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { OpenClawPluginApi } from "openclaw/plugin-sdk";
import type { Logger } from "../logger.js";
import {
  type NotificationIngestResult,
  NotificationStorage,
  type StoredNotification,
} from "../notification/storage.js";
import { RELAY_INTERNAL_HTTP_HEADER } from "../tunnel/http-proxy.js";
import type { RelayTunnelService } from "../tunnel/service.js";
import type {
  HttpNotificationBody,
  NotificationPushParams,
  RawNotification,
} from "../types.js";
import {
  readBody,
  type GatewayMethodRegistrar,
} from "./shared.js";

function newIngestId(): string {
  return `ing_${randomBytes(4).toString("hex")}`;
}

function summarizeIncomingBatch(items: RawNotification[]): string {
  const total = items.length;
  if (total === 0) return "items=0";
  const now = Date.now();
  let earliest = Number.POSITIVE_INFINITY;
  let latest = Number.NEGATIVE_INFINITY;
  for (const n of items) {
    const t = new Date(n.timestamp).getTime();
    if (Number.isNaN(t)) continue;
    if (t < earliest) earliest = t;
    if (t > latest) latest = t;
  }
  if (!Number.isFinite(earliest)) {
    return `items=${total} (all ts invalid)`;
  }
  const maxLag = now - earliest;
  const minLag = now - latest;
  return `items=${total} earliestTs=${new Date(earliest).toISOString()} lag=${minLag}..${maxLag}ms`;
}

function createEmptyIngestResult(): NotificationIngestResult {
  return {
    received: 0,
    ingested: 0,
    dedupedById: 0,
    dedupedByContent: 0,
    invalid: 0,
    inserted: [],
  };
}

function isRelayInternalHttpRequest(req: IncomingMessage): boolean {
  const header = req.headers?.[RELAY_INTERNAL_HTTP_HEADER];
  if (Array.isArray(header)) {
    return header.includes("1");
  }
  return header === "1";
}

/**
 * 将 ingest 内部结果转成对外响应：丢弃 `inserted` 字段，
 * 它只供插件内部下游链路（如 light-rule agent 评估）使用，
 * 不应该出现在 gateway / HTTP 响应里。
 */
function toIngestResponse(
  result: NotificationIngestResult,
): Omit<NotificationIngestResult, "inserted"> {
  const { inserted: _inserted, ...rest } = result;
  return rest;
}

interface NotificationInterfaceDeps {
  api: OpenClawPluginApi;
  logger: Logger;
  getStorage: () => NotificationStorage | null;
  filterNotifications: (items: RawNotification[]) => RawNotification[];
  registerGatewayMethod: GatewayMethodRegistrar;
  tunnelService?: RelayTunnelService | null;
  /** 通知落盘后的钩子，用于触发灯效规则评估。 */
  onAfterIngest?: (
    inserted: StoredNotification[],
    ingestId: string,
  ) => void | Promise<void>;
}

export function registerNotificationInterfaces(
  deps: NotificationInterfaceDeps,
): void {
  const {
    api,
    logger,
    getStorage,
    filterNotifications,
    registerGatewayMethod,
    tunnelService,
    onAfterIngest,
  } = deps;

  function triggerAfterIngest(
    inserted: StoredNotification[],
    ingestId: string,
  ): void {
    if (inserted.length === 0 || !onAfterIngest) return;
    logger.info(
      `notifications[${ingestId}]: enqueue ${inserted.length} for light-rule evaluation`,
    );
    void Promise.resolve()
      .then(() => onAfterIngest(inserted, ingestId))
      .catch((err) =>
        logger.warn(
          `notifications[${ingestId}]: onAfterIngest failed: ${err?.message ?? err}`,
        ),
      );
  }

  registerGatewayMethod(
    "notifications.push",
    async ({ params, respond }) => {
      const storage = getStorage();
      if (!storage) {
        respond(false, null, {
          code: "NOT_READY",
          message: "Storage service not ready",
        });
        return;
      }

      const { items } = params as unknown as NotificationPushParams;
      if (!Array.isArray(items)) {
        respond(false, null, {
          code: "INVALID_PARAMS",
          message: "items must be an array",
        });
        return;
      }

      const ingestId = newIngestId();
      const startMs = Date.now();
      logger.info(
        `notifications[${ingestId}]: gateway notifications.push received ${summarizeIncomingBatch(items)}`,
      );

      const filtered = filterNotifications(items);
      if (filtered.length !== items.length) {
        logger.info(
          `notifications[${ingestId}]: filtered ${items.length - filtered.length} ignored-app item(s), kept=${filtered.length}`,
        );
      }
      const result = filtered.length
        ? await storage.ingest(filtered, ingestId)
        : createEmptyIngestResult();

      logger.info(
        `notifications[${ingestId}]: ingest done in ${Date.now() - startMs}ms ` +
          `(received=${result.received} ingested=${result.ingested} ` +
          `dedupedById=${result.dedupedById} dedupedByContent=${result.dedupedByContent} invalid=${result.invalid})`,
      );

      // 主响应先返回，不阻塞灯效评估
      respond(true, toIngestResponse(result));

      // 落盘后异步触发灯效评估（fire-and-forget）
      triggerAfterIngest(result.inserted, ingestId);
    },
  );

  api.registerHttpRoute({
    path: "/notifications",
    auth: "gateway",
    async handler(req: IncomingMessage, res: ServerResponse) {
      if (req.method !== "POST") {
        res.writeHead(405, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Method Not Allowed" }));
        return;
      }

      if (!isRelayInternalHttpRequest(req)) {
        await tunnelService?.deactivateForExternalTunnel("POST /notifications");
      }

      const storage = getStorage();
      if (!storage) {
        res.writeHead(503, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Service Not Ready" }));
        return;
      }

      let body: HttpNotificationBody;
      try {
        const raw = await readBody(req);
        body = JSON.parse(raw);
      } catch {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "Invalid JSON" }));
        return;
      }

      if (!Array.isArray(body.notifications)) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            ok: false,
            error: "notifications must be an array",
          }),
        );
        return;
      }

      const ingestId = newIngestId();
      const startMs = Date.now();
      logger.info(
        `notifications[${ingestId}]: HTTP POST /notifications received ${summarizeIncomingBatch(body.notifications)}`,
      );

      const filtered = filterNotifications(body.notifications);
      if (filtered.length !== body.notifications.length) {
        logger.info(
          `notifications[${ingestId}]: filtered ${body.notifications.length - filtered.length} ignored-app item(s), kept=${filtered.length}`,
        );
      }
      const result = filtered.length
        ? await storage.ingest(filtered, ingestId)
        : createEmptyIngestResult();

      logger.info(
        `notifications[${ingestId}]: ingest done in ${Date.now() - startMs}ms ` +
          `(received=${result.received} ingested=${result.ingested} ` +
          `dedupedById=${result.dedupedById} dedupedByContent=${result.dedupedByContent} invalid=${result.invalid})`,
      );

      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ ok: true, ...toIngestResponse(result) }));

      // Relay tunnel 会把手机通知转发到 HTTP 端点；这里也要触发事件驱动灯效评估。
      triggerAfterIngest(result.inserted, ingestId);
    },
  });

  logger.info("Gateway 通知方法已注册: notifications.push");
  logger.info("HTTP 通知端点已注册: POST /notifications");
}

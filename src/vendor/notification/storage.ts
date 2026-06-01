import {
  accessSync,
  mkdirSync,
  appendFileSync,
  readdirSync,
  readFileSync,
  writeFileSync,
  existsSync,
  rmSync,
  constants,
} from "node:fs";
import { createHash } from "node:crypto";
import { join } from "node:path";
import type { PluginConfig, RawNotification } from "../types.js";
import { normalizeFeishuFields } from "./feishu-normalize.js";
import { currentClientLabel } from "../ingest-context.js";

interface Logger {
  info: (message: string) => void;
  warn: (message: string) => void;
}

export type NotificationConversationType = "private" | "group";

export interface StorageContext {
  stateDir: string;
  workspaceDir?: string;
}

/** Stored notification entry (JSON format, append-only) */
export interface StoredNotification {
  /** 来源客户端 label；旧数据无该字段，查询时视为 legacy */
  clientLabel?: string;
  /** 包名 */
  appName: string;
  /** 应用显示名（由映射补全），缺省时与 appName 一致 */
  appDisplayName?: string;
  title: string;
  content: string;
  /** ISO 8601 with timezone, e.g. "2026-03-02T08:30:00+08:00" */
  timestamp: string;
  /**
   * 结构化发送人。当前主要用于飞书消息：
   * iOS 通知里 title 通常是发送人，subtitle 用来区分群聊名。
   */
  senderName?: string;
  /** 结构化会话类型。当前主要用于飞书消息区分群聊/私聊。 */
  conversationType?: NotificationConversationType;
  /** 结构化会话名。当前主要用于飞书群聊名（通常来自 subtitle）。 */
  conversationName?: string;
}

const NOTIFICATION_DIR_NAME = "notifications";
const ID_INDEX_DIR_NAME = ".ids";
const CONTENT_KEY_INDEX_DIR_NAME = ".keys";

export interface NotificationIngestResult {
  received: number;
  ingested: number;
  dedupedById: number;
  dedupedByContent: number;
  invalid: number;
  /**
   * 本次 ingest 实际新写入的条目（去重 / 非法之后的产物）。
   * 用于后续事件驱动链路（如 light-rule agent 评估）拿到本次新增的通知列表，
   * 不需要重新读盘。
   */
  inserted: StoredNotification[];
}

type WriteOutcome =
  | { kind: "ingested"; entry: StoredNotification }
  | { kind: "dedupedById" }
  | { kind: "dedupedByContent" }
  | { kind: "invalid" };

export function getStateFallbackNotificationDir(stateDir: string): string {
  return join(stateDir, "plugins", "phone-notifications", NOTIFICATION_DIR_NAME);
}

function computeLagMs(timestamp: string | undefined): number | null {
  if (!timestamp) return null;
  const t = new Date(timestamp).getTime();
  if (Number.isNaN(t)) return null;
  return Date.now() - t;
}

function buildFallbackContent(n: RawNotification): string {
  const body = n.body?.trim();
  if (body) {
    return body;
  }

  const fallback: string[] = [];
  if (n.category) {
    fallback.push(`category:${n.category}`);
  }
  if (n.metadata && Object.keys(n.metadata).length > 0) {
    fallback.push(`metadata:${JSON.stringify(n.metadata)}`);
  }
  return fallback.join(" ; ") || "-";
}

function ensureWritableDirectory(dir: string): boolean {
  try {
    mkdirSync(dir, { recursive: true });
    accessSync(dir, constants.R_OK | constants.W_OK);
    return true;
  } catch {
    return false;
  }
}

export function resolveNotificationStorageDir(
  ctx: StorageContext,
  logger: Pick<Logger, "info" | "warn">,
): string {
  // 优先使用 stateDir（插件私有稳定路径），避免通知随 workspace 切换而分散
  const stateNotifDir = getStateFallbackNotificationDir(ctx.stateDir);

  if (ensureWritableDirectory(stateNotifDir)) {
    logger.info(`通知将写入 stateDir 路径: ${stateNotifDir}`);
    return stateNotifDir;
  }

  // stateDir 不可用时回退到 workspaceDir
  if (ctx.workspaceDir) {
    const workspaceDir = join(ctx.workspaceDir, NOTIFICATION_DIR_NAME);
    if (ensureWritableDirectory(workspaceDir)) {
      logger.warn(
        `stateDir 不可用，通知已回退到 workspace 路径: ${workspaceDir}`,
      );
      return workspaceDir;
    }
  }

  throw new Error(`通知存储目录不可用: ${stateNotifDir}`);
}

/** 包名 → 应用显示名（异步，未命中时内部可拉取后返回）；不提供则落库仅用 appName */
export type ResolveDisplayName = (packageName: string) => Promise<string>;

export class NotificationStorage {
  private dir: string;
  private idIndexDir: string;
  private contentKeyIndexDir: string;
  private idCache = new Map<string, Set<string>>();
  private contentKeyCache = new Map<string, Set<string>>();
  private dateWriteChains = new Map<string, Promise<void>>();
  private resolveDisplayName: ResolveDisplayName | undefined;

  constructor(
    dir: string,
    private config: PluginConfig,
    private logger: Logger,
    resolveDisplayName?: ResolveDisplayName,
  ) {
    this.dir = dir;
    this.idIndexDir = join(dir, ID_INDEX_DIR_NAME);
    this.contentKeyIndexDir = join(dir, CONTENT_KEY_INDEX_DIR_NAME);
    this.resolveDisplayName = resolveDisplayName;
  }

  async init() {
    mkdirSync(this.dir, { recursive: true });
    mkdirSync(this.idIndexDir, { recursive: true });
    rmSync(this.contentKeyIndexDir, { recursive: true, force: true });
    mkdirSync(this.contentKeyIndexDir, { recursive: true });
  }

  async ingest(
    items: RawNotification[],
    ingestId?: string,
  ): Promise<NotificationIngestResult> {
    const result: NotificationIngestResult = {
      received: items.length,
      ingested: 0,
      dedupedById: 0,
      dedupedByContent: 0,
      invalid: 0,
      inserted: [],
    };

    const traceTag = ingestId ? `[${ingestId}]` : "";
    for (const n of items) {
      const outcome = await this.writeNotification(n);
      const lagMs = computeLagMs(n.timestamp);
      const lagPart = lagMs === null ? "lag=?" : `lag=${lagMs}ms`;
      this.logger.info(
        `ingest${traceTag}: app=${n.app ?? "Unknown"} id=${n.id ?? "-"} ts=${n.timestamp} ${lagPart} outcome=${outcome.kind}`,
      );
      switch (outcome.kind) {
        case "ingested":
          result.ingested += 1;
          result.inserted.push(outcome.entry);
          break;
        case "dedupedById":
          result.dedupedById += 1;
          break;
        case "dedupedByContent":
          result.dedupedByContent += 1;
          break;
        case "invalid":
          result.invalid += 1;
          break;
      }
    }
    this.prune();
    return result;
  }

  private async writeNotification(n: RawNotification): Promise<WriteOutcome> {
    const ts = new Date(n.timestamp);
    if (Number.isNaN(ts.getTime())) {
      this.logger.warn(`忽略非法 timestamp 的通知: ${n.id}`);
      return { kind: "invalid" };
    }

    const dateKey = this.formatDate(ts);
    const filePath = join(this.dir, `${dateKey}.json`);
    const normalizedId = typeof n.id === "string" ? n.id.trim() : "";
    const entry = this.buildStoredNotification(n);

    return this.withDateWriteLock(dateKey, async () => {
      if (normalizedId && this.hasNotificationId(dateKey, entry.clientLabel ?? "legacy", normalizedId)) {
        return { kind: "dedupedById" };
      }

      if (this.hasNotificationContentKey(dateKey, filePath, entry)) {
        return { kind: "dedupedByContent" };
      }

      const appDisplayName = this.resolveDisplayName
        ? await this.resolveDisplayName(entry.appName)
        : entry.appName;

      const storedEntry: StoredNotification = {
        ...entry,
        appDisplayName,
      };

      const arr = this.readStoredNotifications(filePath);
      arr.push(storedEntry);
      writeFileSync(filePath, JSON.stringify(arr, null, 2), "utf-8");

      if (normalizedId) {
        this.recordNotificationId(dateKey, storedEntry.clientLabel ?? "legacy", normalizedId);
      }
      this.recordNotificationContentKey(dateKey, filePath, storedEntry);
      return { kind: "ingested", entry: storedEntry };
    });
  }

  private buildStoredNotification(n: RawNotification): StoredNotification {
    const appName = typeof n.app === "string" && n.app ? n.app : "Unknown";
    const clientLabel = currentClientLabel("default");
    const feishu = normalizeFeishuFields(n);
    if (feishu) {
      return {
        clientLabel,
        appName,
        title: feishu.title,
        content: feishu.content || buildFallbackContent(n),
        timestamp: n.timestamp,
        ...feishu.structured,
      };
    }
    return {
      clientLabel,
      appName,
      title: typeof n.title === "string" ? n.title : "",
      content: buildFallbackContent(n),
      timestamp: n.timestamp,
    };
  }

  private formatDate(d: Date): string {
    const year = d.getFullYear();
    const month = String(d.getMonth() + 1).padStart(2, "0");
    const day = String(d.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
  }

  private getIdIndexPath(dateKey: string): string {
    return join(this.idIndexDir, `${dateKey}.ids`);
  }

  private getIdSet(dateKey: string): Set<string> {
    const cached = this.idCache.get(dateKey);
    if (cached) {
      return cached;
    }

    const idPath = this.getIdIndexPath(dateKey);
    const ids = new Set<string>();
    if (existsSync(idPath)) {
      const lines = readFileSync(idPath, "utf-8").split(/\r?\n/);
      for (const line of lines) {
        const id = line.trim();
        if (id) {
          ids.add(id);
        }
      }
    }

    this.idCache.set(dateKey, ids);
    return ids;
  }

  private buildNotificationIdKey(clientLabel: string, id: string): string {
    if (this.usesLegacyIdKey(clientLabel)) {
      return id;
    }
    return `${clientLabel}\x1f${id}`;
  }

  private usesLegacyIdKey(clientLabel: string): boolean {
    return clientLabel === "legacy" || clientLabel === "default";
  }

  private hasNotificationId(dateKey: string, clientLabel: string, id: string): boolean {
    const ids = this.getIdSet(dateKey);
    return ids.has(this.buildNotificationIdKey(clientLabel, id))
      || (this.usesLegacyIdKey(clientLabel) && ids.has(id));
  }

  private getContentKeyIndexPath(dateKey: string): string {
    return join(this.contentKeyIndexDir, `${dateKey}.keys`);
  }

  private getContentKeySet(dateKey: string, filePath: string): Set<string> {
    const cached = this.contentKeyCache.get(dateKey);
    if (cached) {
      return cached;
    }

    const keyPath = this.getContentKeyIndexPath(dateKey);
    const keys = new Set<string>();

    if (existsSync(filePath)) {
      for (const item of this.readStoredNotifications(filePath)) {
        keys.add(this.buildNotificationContentKey(item));
      }
    }

    if (keys.size > 0) {
      writeFileSync(keyPath, `${Array.from(keys).join("\n")}\n`, "utf-8");
    } else if (existsSync(keyPath)) {
      rmSync(keyPath, { force: true });
    }

    this.contentKeyCache.set(dateKey, keys);
    return keys;
  }

  private hasNotificationContentKey(
    dateKey: string,
    filePath: string,
    entry: Pick<StoredNotification, "appName" | "title" | "content" | "timestamp" | "clientLabel">,
  ): boolean {
    const keys = this.getContentKeySet(dateKey, filePath);
    return this.contentKeyLabels(entry.clientLabel).some((clientLabel) =>
      keys.has(this.buildNotificationContentKey({ ...entry, clientLabel })),
    );
  }

  private recordNotificationId(dateKey: string, clientLabel: string, id: string) {
    const ids = this.getIdSet(dateKey);
    const key = this.buildNotificationIdKey(clientLabel, id);
    if (ids.has(key)) {
      return;
    }

    appendFileSync(this.getIdIndexPath(dateKey), `${key}\n`, "utf-8");
    ids.add(key);
  }

  private recordNotificationContentKey(
    dateKey: string,
    filePath: string,
    entry: Pick<StoredNotification, "appName" | "title" | "content" | "timestamp" | "clientLabel">,
  ) {
    const keys = this.getContentKeySet(dateKey, filePath);
    const key = this.buildNotificationContentKey(entry);

    if (keys.has(key)) {
      return;
    }

    appendFileSync(this.getContentKeyIndexPath(dateKey), `${key}\n`, "utf-8");
    keys.add(key);
  }

  private buildNotificationContentKey(
    entry: Pick<StoredNotification, "appName" | "title" | "content" | "timestamp" | "clientLabel">,
  ): string {
    return createHash("sha256")
      .update(entry.clientLabel ?? "legacy")
      .update("\x1f")
      .update(entry.appName)
      .update("\x1f")
      .update(entry.title)
      .update("\x1f")
      .update(entry.content)
      .update("\x1f")
      .update(entry.timestamp)
      .digest("hex");
  }

  private contentKeyLabels(clientLabel: string | undefined): string[] {
    const label = clientLabel ?? "legacy";
    return label === "default" ? ["default", "legacy"] : [label];
  }

  private readStoredNotifications(filePath: string): StoredNotification[] {
    if (!existsSync(filePath)) {
      return [];
    }

    try {
      const parsed = JSON.parse(readFileSync(filePath, "utf-8"));
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  }

  private async withDateWriteLock<T>(
    dateKey: string,
    task: () => Promise<T>,
  ): Promise<T> {
    const previous = this.dateWriteChains.get(dateKey) ?? Promise.resolve();
    let release!: () => void;
    const current = new Promise<void>((resolve) => {
      release = resolve;
    });
    const chain = previous.then(() => current);
    this.dateWriteChains.set(dateKey, chain);

    await previous;
    try {
      return await task();
    } finally {
      release();
      if (this.dateWriteChains.get(dateKey) === chain) {
        this.dateWriteChains.delete(dateKey);
      }
    }
  }

  private prune() {
    const retentionDays = this.config.retentionDays;
    if (retentionDays === undefined) {
      return;
    }

    const cutoffMs = Date.now() - retentionDays * 24 * 60 * 60 * 1000;
    const cutoffDate = this.formatDate(new Date(cutoffMs));

    this.pruneDataFiles(cutoffDate);
    this.pruneIdIndex(cutoffDate);
    this.pruneContentKeyIndex(cutoffDate);
  }

  /** Remove expired .json, legacy .md files, and legacy date directories */
  private pruneDataFiles(cutoffDate: string) {
    const dateFilePattern = /^(\d{4}-\d{2}-\d{2})\.(json|md)$/;
    const dateDirPattern = /^\d{4}-\d{2}-\d{2}$/;

    try {
      for (const entry of readdirSync(this.dir, { withFileTypes: true })) {
        if (entry.isFile()) {
          const match = dateFilePattern.exec(entry.name);
          if (match && match[1] < cutoffDate) {
            rmSync(join(this.dir, entry.name), { force: true });
          }
        } else if (entry.isDirectory() && dateDirPattern.test(entry.name) && entry.name < cutoffDate) {
          rmSync(join(this.dir, entry.name), { recursive: true, force: true });
        }
      }
    } catch {
      // ignore
    }
  }

  /** Remove expired .ids index files */
  private pruneIdIndex(cutoffDate: string) {
    try {
      for (const entry of readdirSync(this.idIndexDir, { withFileTypes: true })) {
        if (!entry.isFile()) continue;
        const match = /^(\d{4}-\d{2}-\d{2})\.ids$/.exec(entry.name);
        if (match && match[1] < cutoffDate) {
          rmSync(join(this.idIndexDir, entry.name), { force: true });
          this.idCache.delete(match[1]);
        }
      }
    } catch {
      // ignore
    }
  }

  private pruneContentKeyIndex(cutoffDate: string) {
    try {
      for (const entry of readdirSync(this.contentKeyIndexDir, { withFileTypes: true })) {
        if (!entry.isFile()) continue;
        const match = /^(\d{4}-\d{2}-\d{2})\.keys$/.exec(entry.name);
        if (match && match[1] < cutoffDate) {
          rmSync(join(this.contentKeyIndexDir, entry.name), { force: true });
          this.contentKeyCache.delete(match[1]);
        }
      }
    } catch {
      // ignore
    }
  }

  async close() {
    this.idCache.clear();
    this.contentKeyCache.clear();
    this.dateWriteChains.clear();
  }
}

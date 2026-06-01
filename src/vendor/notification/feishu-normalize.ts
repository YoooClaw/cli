import type { RawNotification } from "../types.js";
import type { NotificationConversationType } from "./storage.js";

export interface FeishuStructuredFields {
  senderName?: string;
  conversationType?: NotificationConversationType;
  conversationName?: string;
}

export interface FeishuNormalizedNotification {
  title: string;
  content: string;
  structured: FeishuStructuredFields;
}

function normalizeOptionalText(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed ? trimmed : undefined;
}

function isFeishuApp(appName: string): boolean {
  const normalized = appName.trim().toLowerCase();
  if (!normalized) {
    return false;
  }
  return (
    normalized === "飞书"
    || normalized === "feishu"
    || normalized === "lark"
    || normalized === "com.ss.android.lark"
    || normalized === "com.bytedance.ee.lark"
    || normalized === "com.larksuite.suite"
    || normalized.includes("feishu")
    || normalized.includes("lark")
    || normalized.includes("飞书")
  );
}

function isFeishuAppLabel(text: string): boolean {
  const t = text.trim().toLowerCase();
  return t === "飞书" || t === "lark" || t === "feishu";
}

function extractColonSender(body: string): string | undefined {
  if (!body) return undefined;
  const candidates = [body.indexOf(":"), body.indexOf("：")].filter((i) => i > 0);
  if (candidates.length === 0) return undefined;
  const idx = Math.min(...candidates);
  const senderName = body.slice(0, idx).trim();
  return senderName || undefined;
}

function findMetadataTextByKey(
  value: unknown,
  targetKey: string,
  depth = 0,
): { found: boolean; value?: string } {
  if (!value || typeof value !== "object" || depth > 4) {
    return { found: false };
  }

  if (Array.isArray(value)) {
    for (const item of value) {
      const match = findMetadataTextByKey(item, targetKey, depth + 1);
      if (match.found) {
        return match;
      }
    }
    return { found: false };
  }

  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    if (key.trim().toLowerCase() === targetKey) {
      return { found: true, value: normalizeOptionalText(child) };
    }
  }

  for (const child of Object.values(value as Record<string, unknown>)) {
    const match = findMetadataTextByKey(child, targetKey, depth + 1);
    if (match.found) {
      return match;
    }
  }

  return { found: false };
}

function deriveStructured(n: RawNotification): FeishuStructuredFields {
  const subtitle = findMetadataTextByKey(n.metadata, "subtitle");

  // iOS 路径：通知元数据带 subtitle key（群聊为群名，私聊为空）。
  if (subtitle.found) {
    const senderName = normalizeOptionalText(n.title);
    const structured: FeishuStructuredFields = {
      conversationType: subtitle.value ? "group" : "private",
    };
    if (senderName) {
      structured.senderName = senderName;
    }
    if (subtitle.value) {
      structured.conversationName = subtitle.value;
    }
    return structured;
  }

  // Android 路径：title 退化为 app 名（如 "飞书"），实际发送人在 body 里以 "name: content" 形式出现。
  const rawTitle = typeof n.title === "string" ? n.title : "";
  if (isFeishuAppLabel(rawTitle)) {
    const senderName = extractColonSender(n.body ?? "");
    if (senderName) {
      return {
        senderName,
        conversationType: "private",
      };
    }
  }

  return {};
}

function buildTitle(n: RawNotification, structured: FeishuStructuredFields): string {
  if (structured.conversationType === "group" && structured.conversationName) {
    return structured.conversationName;
  }
  // Android 私聊 title 是 app 名，用 body 解析出的 senderName 顶上去，跟 iOS 对齐。
  if (structured.conversationType === "private" && structured.senderName) {
    return structured.senderName;
  }
  return typeof n.title === "string" ? n.title : "";
}

function buildContent(n: RawNotification, structured: FeishuStructuredFields): string {
  const body = n.body?.trim();

  if (structured.conversationType === "group" && structured.senderName && body) {
    const half = `${structured.senderName}:`;
    const full = `${structured.senderName}：`;
    return body.startsWith(half) || body.startsWith(full)
      ? body
      : `${structured.senderName}: ${body}`;
  }

  if (structured.conversationType === "private" && structured.senderName && body) {
    const half = `${structured.senderName}:`;
    const full = `${structured.senderName}：`;
    if (body.startsWith(half)) return body.slice(half.length).trimStart();
    if (body.startsWith(full)) return body.slice(full.length).trimStart();
    return body;
  }

  return body ?? "";
}

export function normalizeFeishuFields(
  n: RawNotification,
): FeishuNormalizedNotification | undefined {
  const appName = typeof n.app === "string" ? n.app : "";
  if (!isFeishuApp(appName)) {
    return undefined;
  }

  const structured = deriveStructured(n);

  // Feishu 通知但任何规则都没命中：让 caller 走默认归一化路径，避免与非 Feishu 通知行为分裂。
  if (
    structured.senderName === undefined
    && structured.conversationType === undefined
    && structured.conversationName === undefined
  ) {
    return undefined;
  }

  return {
    title: buildTitle(n, structured),
    content: buildContent(n, structured),
    structured,
  };
}

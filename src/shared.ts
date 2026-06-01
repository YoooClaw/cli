/**
 * 共享 src 适配层 —— CLI 与 openclaw 插件**共用** phone-notifications 的 `src/`。
 *
 * 跨包相对路径只在本文件出现，其余 CLI 代码从这里 import，避免到处写 `../../phone-notifications/...`。
 * 这些模块是纯函数/纯存储逻辑（不依赖 openclaw 宿主），Bun 构建时会被打进 CLI 产物。
 */
export {
  collectMatchingNotifications,
  matchesNotificationQuery,
  type NotificationQueryOptions,
} from "./vendor/cli/ntf-query.js";

export type {
  StoredNotification,
  NotificationConversationType,
} from "./vendor/notification/storage.js";

export {
  listDateKeysAsync,
  readDateFileAsync,
  matchesNotificationAppFilter,
  sortNotificationsByTimestampDesc,
  readRecordingIndex,
  daysAgo,
  today,
  formatDate,
} from "./vendor/cli/helpers.js";

export type { CliRecordingEntry } from "./vendor/cli/helpers.js";

// ── daemon 侧复用（接收手机推送 + 灯效规则 + 灯效校验）──
export { NotificationStorage } from "./vendor/notification/storage.js";
export { registerNotificationInterfaces } from "./vendor/plugin/notifications.js";
export { RecordingStorage } from "./vendor/recording/storage.js";
export { registerRecordingInterfaces } from "./vendor/plugin/recordings.js";
export { handleRecordingSync } from "./vendor/recording/handler.js";
export type { RecordingStatusEvent } from "./vendor/recording/handler.js";
export { validateAsrConfig } from "./vendor/recording/asr.js";
export type {
  AsrConfig,
  RecordingMetadata,
} from "./vendor/types.js";
export { LightRuleRegistry } from "./vendor/light-rules/registry.js";
export { registerLightRulesGateway } from "./vendor/light-rules/gateway.js";
export {
  LIGHT_RULE_GATEWAY_METHODS,
  LIGHT_RULE_GATEWAY_METHOD_LIST,
} from "./vendor/light-rules/names.js";
export { validateSegments } from "./vendor/light/validators.js";
export { normalizeRepeatTimes, assertAncsRepeatTimes } from "./vendor/light/repeat.js";
export type { LightSegment, PluginConfig, RawNotification } from "./vendor/types.js";
export { sendLightEffect } from "./vendor/light/sender.js";
export type { SendLightResult } from "./vendor/light/sender.js";

import presetsV1Raw from "./vendor/light/presets-v1.json";
import type { LightSegment as _LightSegment } from "./vendor/types.js";

export interface LightPreset {
  presetId: string;
  displayName: string;
  description: string;
  repeat_times: number;
  segments: _LightSegment[];
}

/** phone-notifications 共享的内置 preset 表（red-steady / red-breath / red-strobe-3 / green-* / blue-* / yellow-strobe-3）。 */
export const LIGHT_PRESETS_V1 = presetsV1Raw as {
  version: string;
  presets: LightPreset[];
};

// ── Relay 隧道：复用插件的连接逻辑（RelayClient 建连/心跳/重连 + TunnelProxy 反代到本地 HTTP）──
export { RelayClient } from "./vendor/tunnel/relay-client.js";
export type { TunnelStatusInfo } from "./vendor/tunnel/relay-client.js";
export { TunnelProxy } from "./vendor/tunnel/proxy.js";
export {
  currentClientLabel,
  getIngestContext,
  runWithIngestContext,
  type IngestContext,
} from "./vendor/ingest-context.js";
export {
  RELAY_INTERNAL_HTTP_HEADER,
  mapPath as mapRelayHttpPath,
} from "./vendor/tunnel/http-proxy.js";

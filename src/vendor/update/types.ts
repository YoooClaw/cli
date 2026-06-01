export interface UpdateInfo {
  current: string;
  latest: string;
}

export interface ExternalUpdateNotifier {
  notifyUpdateAvailable: (update: UpdateInfo) => void;
  clearPendingUpdate: () => void;
}

export interface AutoUpdateConfig {
  /** 是否启用自动更新检查，默认 true */
  enabled?: boolean;
  /** 检查间隔（小时），默认 4 */
  checkIntervalHours?: number;
  /** 更新频道：latest 仅检查正式版，beta 包含预发布版；production 环境会强制使用 latest */
  channel?: "latest" | "beta";
  /** 更新制品源 base URL；未设置时 production 走正式 OSS，test 走测试 OSS */
  baseUrl?: string;
}

/** 与 OpenClaw gateway broadcast 签名对齐 */
export type BroadcastFn = (
  event: string,
  payload: unknown,
) => void;

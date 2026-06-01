/**
 * 录音传输状态机（约束 R-3）
 *
 * 状态流转严格不可跳跃：
 *   receiving_failed → receiving
 *   receiving → pending_oss_upload
 *   pending_oss_upload → uploading_oss
 *   uploading_oss → oss_uploaded
 *   oss_uploaded → syncing_openclaw
 *   syncing_openclaw → synced | sync_failed
 *   sync_failed → syncing_openclaw
 *   synced → transcribing（仅当已配置 ASR）
 *   transcribing → transcribed | transcribe_failed
 *   transcribe_failed → transcribing
 *   transcribed → transcribing（手动强制重新转写）
 */

import type { RecordingTransferStatus } from "../types.js";

/** 合法的状态转换表：from → Set<to> */
const VALID_TRANSITIONS: ReadonlyMap<
  RecordingTransferStatus,
  ReadonlySet<RecordingTransferStatus>
> = new Map<RecordingTransferStatus, ReadonlySet<RecordingTransferStatus>>([
  ["receiving_failed", new Set(["receiving"])],
  ["receiving", new Set(["pending_oss_upload"])],
  ["pending_oss_upload", new Set(["uploading_oss"])],
  ["uploading_oss", new Set(["oss_uploaded"])],
  ["oss_uploaded", new Set(["syncing_openclaw"])],
  ["syncing_openclaw", new Set(["synced", "sync_failed"])],
  ["sync_failed", new Set(["syncing_openclaw"])],
  ["synced", new Set(["transcribing"])],
  ["transcribing", new Set(["transcribed", "transcribe_failed"])],
  ["transcribe_failed", new Set(["transcribing"])],
  ["transcribed", new Set(["transcribing"])],
]);

/** 插件侧关注的状态：从 syncing_openclaw 开始 */
export type PluginSideStatus = Extract<
  RecordingTransferStatus,
  | "syncing_openclaw"
  | "sync_failed"
  | "synced"
  | "transcribing"
  | "transcribe_failed"
  | "transcribed"
>;

/**
 * 校验状态转换是否合法
 */
export function isValidTransition(
  from: RecordingTransferStatus,
  to: RecordingTransferStatus,
): boolean {
  const allowed = VALID_TRANSITIONS.get(from);
  return allowed ? allowed.has(to) : false;
}

/**
 * 尝试执行状态转换，非法转换抛出 Error
 */
export function transition(
  from: RecordingTransferStatus,
  to: RecordingTransferStatus,
): RecordingTransferStatus {
  if (!isValidTransition(from, to)) {
    throw new TransitionError(from, to);
  }
  return to;
}

/**
 * 状态是否为终态（没有后续合法转换）
 */
export function isTerminalStatus(status: RecordingTransferStatus): boolean {
  const allowed = VALID_TRANSITIONS.get(status);
  return !allowed || allowed.size === 0;
}

/**
 * 状态是否允许触发 ASR 转写
 */
export function canStartTranscription(status: RecordingTransferStatus): boolean {
  return (
    status === "synced"
    || status === "transcribe_failed"
    || status === "transcribed"
  );
}

/**
 * 获取当前状态的所有合法后续状态
 */
export function getNextStatuses(
  status: RecordingTransferStatus,
): RecordingTransferStatus[] {
  const allowed = VALID_TRANSITIONS.get(status);
  return allowed ? [...allowed] : [];
}

export class TransitionError extends Error {
  constructor(
    public readonly from: RecordingTransferStatus,
    public readonly to: RecordingTransferStatus,
  ) {
    super(`非法状态转换: ${from} → ${to}`);
    this.name = "TransitionError";
  }
}

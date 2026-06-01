/**
 * 结构化错误码 —— 进入半正式契约，命名前缀统一 `YOOOCLAW_*`。
 * 命令失败时统一以 `{ ok: false, error: { code, message, ... } }` 输出（见 output/format.ts）。
 */
export const ErrorCode = {
  /** 通用 / 未归类错误 */
  UNKNOWN: "YOOOCLAW_UNKNOWN",
  /** 参数校验失败 */
  INVALID_ARGUMENT: "YOOOCLAW_INVALID_ARGUMENT",
  /** 该命令尚未实现（脚手架阶段占位） */
  NOT_IMPLEMENTED: "YOOOCLAW_NOT_IMPLEMENTED",
  /** daemon 未运行 */
  DAEMON_NOT_RUNNING: "YOOOCLAW_DAEMON_NOT_RUNNING",
  /** daemon 已在运行（单例保护） */
  DAEMON_ALREADY_RUNNING: "YOOOCLAW_DAEMON_ALREADY_RUNNING",
  /** 鉴权失败 / token 不一致 */
  UNAUTHORIZED: "YOOOCLAW_UNAUTHORIZED",
  /** 配置缺失或非法 */
  CONFIG_INVALID: "YOOOCLAW_CONFIG_INVALID",
  /** profile 不存在 */
  PROFILE_NOT_FOUND: "YOOOCLAW_PROFILE_NOT_FOUND",
  /** 图片尚未下载完成 */
  IMAGE_NOT_READY: "YOOOCLAW_IMAGE_NOT_READY",
  /** 资源未找到 */
  NOT_FOUND: "YOOOCLAW_NOT_FOUND",
  /** 资源已存在（如 profile 重名） */
  ALREADY_EXISTS: "YOOOCLAW_ALREADY_EXISTS",
  /** 存储目录不可用 */
  STORAGE_UNAVAILABLE: "YOOOCLAW_STORAGE_UNAVAILABLE",
  /** 凭据缺失 */
  CREDENTIAL_MISSING: "YOOOCLAW_CREDENTIAL_MISSING",
  /** OS keychain 不可用 */
  KEYCHAIN_UNAVAILABLE: "YOOOCLAW_KEYCHAIN_UNAVAILABLE",
  /** api-key label 不合法 */
  APIKEY_LABEL_INVALID: "YOOOCLAW_APIKEY_LABEL_INVALID",
  /** api-key label 已存在 */
  APIKEY_LABEL_DUPLICATE: "YOOOCLAW_APIKEY_LABEL_DUPLICATE",
  /** api-key label 不存在 */
  APIKEY_LABEL_NOT_FOUND: "YOOOCLAW_APIKEY_LABEL_NOT_FOUND",
  /** multi-key 模式不支持 keychain 写入 */
  KEYCHAIN_MULTI_UNSUPPORTED: "YOOOCLAW_KEYCHAIN_MULTI_UNSUPPORTED",
  /** tunnel label 不存在 */
  TUNNEL_LABEL_NOT_FOUND: "YOOOCLAW_TUNNEL_LABEL_NOT_FOUND",
  /** 网络 / 出站调用失败 */
  NETWORK_ERROR: "YOOOCLAW_NETWORK_ERROR",
  /** 操作被用户取消或需要确认 */
  CONFIRMATION_REQUIRED: "YOOOCLAW_CONFIRMATION_REQUIRED",
  /** 需要 TTY 交互但当前非交互环境 */
  NOT_INTERACTIVE: "YOOOCLAW_NOT_INTERACTIVE",
} as const;

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

export interface YoooclawErrorDetails {
  hint?: string;
  checkedPaths?: string[];
  [key: string]: unknown;
}

/**
 * 携带结构化错误码的错误。CLI 顶层捕获后按 `--format` 序列化为统一错误 schema。
 */
export class YoooclawError extends Error {
  readonly code: ErrorCodeValue;
  readonly details: YoooclawErrorDetails;
  /** 进程退出码，默认 1 */
  readonly exitCode: number;

  constructor(
    code: ErrorCodeValue,
    message: string,
    details: YoooclawErrorDetails = {},
    exitCode = 1,
  ) {
    super(message);
    this.name = "YoooclawError";
    this.code = code;
    this.details = details;
    this.exitCode = exitCode;
  }

  toErrorPayload(): {
    code: ErrorCodeValue;
    message: string;
  } & YoooclawErrorDetails {
    return { code: this.code, message: this.message, ...this.details };
  }
}

export function notImplemented(command: string): YoooclawError {
  return new YoooclawError(
    ErrorCode.NOT_IMPLEMENTED,
    `命令 \`${command}\` 尚未实现`,
    { hint: "该命令处于脚手架阶段，后续按 imp 文档逐步落地" },
  );
}

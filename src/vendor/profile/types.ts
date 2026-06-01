/**
 * 宿主 Profile 抽象。
 *
 * 插件需要适配多种 "claw" 宿主（本地 openclaw、arkclaw、阿里云 jvsclaw），
 * 它们的差异主要集中在认证/pair 侧。这里把所有随宿主变化的能力收进
 * 一个 ClawProfile 对象，启动时只选择一次，其余代码依赖接口而非字符串分支。
 *
 * 第 1 步（纯重构）：先落地类型骨架与路径/检测能力，
 * AuthProvider / endpoints 的具体实现分别在后续步骤接入。
 */

/** 新的宿主渠道标识。qclaw 在路径检测层面仍单独存在，但在 Profile 层归为 openclaw 家族。 */
export type ClawKind = "openclaw" | "arkclaw" | "jvsclaw";

/** 旧的宿主种别（路径/状态目录家族检测用），保持向后兼容。 */
export type HostKind = "openclaw" | "qclaw";

/** relay/HTTP 连接所需的认证凭据，由 AuthProvider 生成。 */
export interface AuthCredential {
  /** 拼到 URL 上的 query 参数，例如 { apiKey } */
  query?: Record<string, string>;
  /** 请求头，例如 { "X-Api-Key-Id": ... } 或 { Authorization } */
  headers?: Record<string, string>;
}

/** 认证状态，供 doctor / 启动守卫使用。 */
export interface AuthStatus {
  ready: boolean;
  /** 已配置的 apiKey 预览（脱敏），未配置时为 undefined */
  apiKeyPreview?: string;
  /** 当无法就绪时的人类可读原因 */
  reason?: string;
}

/**
 * 认证/pair 抽象。各宿主的差异全部收敛在此接口的实现里。
 *
 * - openclaw / arkclaw：apiKey 由 `ntf auth set-api-key` 写入，ensureProvisioned 为 no-op
 * - jvsclaw：ensureProvisioned 读取宿主下发的密文 → 调用 instance/ready 置换 apiKey → 落盘
 */
export interface AuthProvider {
  kind: ClawKind;

  /**
   * 确保本地已具备可用 apiKey。relay 服务在连接前 await 它。
   * jvsclaw 实现会重试直到置换成功（失败则不放行后续连接）；
   * abortSignal 触发时（如插件停止）应中止重试并返回。
   */
  ensureProvisioned(abortSignal?: AbortSignal): Promise<void>;

  /** 当前认证状态（doctor / 启动守卫）。 */
  status(): Promise<AuthStatus>;

  /** 生成一次 relay/HTTP 连接所需凭据，内部可吸收 token 刷新/签名。 */
  getRelayCredential(): Promise<AuthCredential>;

  /** 清除本地认证信息。 */
  clear(): Promise<void>;

  /** 监听认证信息变化（如 set-api-key 后），返回取消监听的函数。 */
  watch(onChange: () => void): () => void;
}

/** 宿主的路径解析能力。 */
export interface ClawPaths {
  /** 宿主原始状态目录，不保证可写。 */
  resolveHostStateDir(stateDir?: string): string;
  /** 宿主原始配置文件路径，不保证可写。 */
  resolveHostConfigPath(stateDir?: string): string;
  /** 插件可写状态目录。 */
  resolveStateDir(hostStateDir?: string): string;
  /** 插件应使用的配置文件路径。 */
  resolveConfigPath(hostStateDir?: string): string;
  /** 插件可写状态目录中的文件。 */
  resolveStateFile(filename: string, hostStateDir?: string): string;
}

/**
 * 单一宿主 Profile，聚合随渠道变化的全部能力。
 * 第 1 步仅落地 kind / paths；auth、endpoints 在后续步骤补全。
 */
export interface ClawProfile {
  kind: ClawKind;
  paths: ClawPaths;
}

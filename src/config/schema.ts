/**
 * yoooclaw daemon 配置 schema（per-profile `config.json`）。
 *
 * 对齐 PRD「数据模型 / config.json 结构」。敏感字段（gateway token / webhook secret）
 * 不直接落在这里，而是用 `*Ref` 抽象引用指向 credentials 文件 / keychain / env。
 */

export const CONFIG_VERSION = 1;

export type LogLevel = "error" | "warn" | "info" | "debug" | "trace";
export type OutputDefaultFormat = "auto" | "json" | "pretty" | "table" | "ndjson";

export interface DaemonSection {
  bind: string;
  port: number;
  logLevel: LogLevel;
  detach: boolean;
}

export interface AuthSection {
  mode: "token";
  /** gateway token 的抽象引用（env: / file: / keychain: / inline:）。 */
  tokenRef: string;
}

export interface RelaySection {
  url: string;
  heartbeatSec: number;
  reconnectBackoffMs: number;
  enabled: boolean;
}

export interface NotificationSection {
  /** 本地保留天数；null 表示永久保存。 */
  retentionDays: number | null;
  ignoredApps: string[];
}

export interface WebhookEvaluatorSection {
  mode: "webhook";
  webhookUrl: string;
  /** webhook secret 的抽象引用。 */
  webhookSecretRef: string;
  timeoutMs: number;
  retries: number;
}

export interface LightRulesSection {
  enabled: boolean;
  evaluator?: WebhookEvaluatorSection;
}

export interface AutoUpdateSection {
  enabled: boolean;
  channel: "stable" | "beta";
}

export interface OutputSection {
  defaultFormat: OutputDefaultFormat;
}

export interface ImageSection {
  /** 单文件下载上限（字节），超限拒绝下载并标记 sync_failed。 */
  maxBytes: number;
}

export interface YoooclawConfig {
  version: number;
  daemon: DaemonSection;
  auth: AuthSection;
  relay: RelaySection;
  notification: NotificationSection;
  lightRules: LightRulesSection;
  autoUpdate: AutoUpdateSection;
  output: OutputSection;
  image: ImageSection;
}

/**
 * 默认 Relay 隧道地址 —— 复用 phone-notifications 插件那套托管 Relay 服务。
 * host 在构建期由 OPENCLAW_HOST_PRODUCTION 注入（见 scripts/build.ts），与插件 env.ts 同源；
 * 未注入时回退到生产域名字面量，保证脱离构建链路也能直连。
 */
const RELAY_HOST = process.env.OPENCLAW_HOST_PRODUCTION || "openclaw-service.yoooclaw.com";
export const DEFAULT_RELAY_URL = `wss://${RELAY_HOST}/message/messages/ws/plugin`;
export const DEFAULT_PORT = 18789;
export const DEFAULT_BIND = "127.0.0.1";
export const DEFAULT_IMAGE_MAX_BYTES = 20 * 1024 * 1024;

/** 生成某个 profile 的默认 config。tokenRef/secretRef 默认指向该 profile 的 credentials 文件。 */
export function defaultConfig(credentialsPath: string): YoooclawConfig {
  return {
    version: CONFIG_VERSION,
    daemon: {
      bind: DEFAULT_BIND,
      port: DEFAULT_PORT,
      logLevel: "info",
      detach: true,
    },
    auth: {
      mode: "token",
      tokenRef: `file:${credentialsPath}#gatewayToken`,
    },
    relay: {
      url: DEFAULT_RELAY_URL,
      heartbeatSec: 10,
      reconnectBackoffMs: 2000,
      enabled: true,
    },
    notification: {
      retentionDays: null,
      ignoredApps: [],
    },
    lightRules: {
      enabled: true,
    },
    autoUpdate: {
      enabled: true,
      channel: "stable",
    },
    output: {
      defaultFormat: "auto",
    },
    image: {
      maxBytes: DEFAULT_IMAGE_MAX_BYTES,
    },
  };
}

/** 默认 webhook 评估器（config init 启用灯效评估时填充）。 */
export function defaultEvaluator(credentialsPath: string): WebhookEvaluatorSection {
  return {
    mode: "webhook",
    webhookUrl: "",
    webhookSecretRef: `file:${credentialsPath}#evaluatorSecret`,
    timeoutMs: 5000,
    retries: 1,
  };
}

/** config.json 里需要遮罩展示的敏感引用字段（点号路径）。 */
export const SECRET_REF_PATHS = [
  "auth.tokenRef",
  "lightRules.evaluator.webhookSecretRef",
];

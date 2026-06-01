import type { RelayTunnelConfig } from "./tunnel/types.js";
import type { AutoUpdateConfig } from "./update/types.js";

export interface PluginConfig {
  /** 通知数据本地保留天数；未设置则永久保存 */
  retentionDays?: number;
  /** 要忽略的 app 包名列表 */
  ignoredApps?: string[];
  /** Relay Tunnel 配置 */
  relay?: RelayTunnelConfig;
  /** ASR 语音转文字配置（录音功能，已废弃；录音链路改为客户端请求级传参） */
  asr?: AsrConfig;
  /** 自动更新配置 */
  autoUpdate?: AutoUpdateConfig;
  /**
   * jvsclaw 宿主在实例启动时写入的密文；插件用它向 claw-manager/instance/ready 换取 apiKey。
   * openclaw.json 路径：plugins.entries.phone-notifications.config.link_secret。
   */
  link_secret?: string;
}

export interface RawNotification {
  id: string;
  app: string;
  title: string;
  body: string;
  /** ISO 8601 含时区，如 "2026-03-02T08:30:00+08:00" */
  timestamp: string;
  category?: string;
  metadata?: Record<string, unknown>;
}

/** Gateway Native 模式：notifications.push 方法的参数 */
export interface NotificationPushParams {
  items: RawNotification[];
}

/** HTTP 备选模式：POST /notifications 的请求体 */
export interface HttpNotificationBody {
  notifications: RawNotification[];
}

/** Gateway Tool：light_control 方法的参数（遵循架构约束 H-0 / H-2） */
export interface LightControlInput {
  /** 1–12 segments, executed in order */
  segments: LightSegment[];
  /** 对本次亮灯的简短标题；不填则回退到 reason */
  title?: string;
  /** 结合对话上下文，简要说明为什么要亮灯 */
  reason: string;
  /** ANCS 路径：0 = infinite loop (\u009B), 1 = play once (\u009A); N>=2 需非 ANCS 路径 */
  repeat_times?: number;
  /** 兼容旧参数：true = infinite loop, false/undefined = play once */
  repeat?: boolean;
}

export interface LightPixelFramePixel {
  /** LED 组索引，0–6 */
  index: number;
  color: { r: number; g: number; b: number };
  /** 0–255；0 会编码成 v11，表示该像素熄灭 */
  brightness: number;
}

export interface LightSegment {
  mode: "wave" | "breath" | "strobe" | "steady" | "color_flow" | "pixel_frame";
  /** seconds; 0 = infinite duration (v11). quantized to 0.5/1/2/3/5/6/8/16/24/32/48 */
  duration_s: number;
  /** 0–255; quantized to 32/64/96/128/192/255. brightness=0 only valid in steady mode (explicit off) */
  brightness?: number;
  /** Non-pixel-frame foreground RGB; color_flow uses it as the first palette anchor */
  color?: { r: number; g: number; b: number };
  /** ms, default 200; wave/strobe/color_flow only */
  interval_ms?: number;
  /** wave/color_flow direction, default "ltr" */
  direction?: "ltr" | "rtl";
  /** wave window LED count (1-3), default 2; color_flow: controls palette shift stride (speed) */
  window?: 1 | 2 | 3;
  /** breath four-phase timing (ms); each quantized to 1040/1560/2080/2600/3100/4160 */
  breath_timing?: {
    rise_ms?: number;
    hold_ms?: number;
    fall_ms?: number;
    off_ms?: number;
  };
  /** background color; wave: background layer; color_flow: second color anchor for palette interpolation */
  background?: { r: number; g: number; b: number; brightness: number };
  /** Pixel-frame payload: M,D,C,(Idx,R,G,B,Br)* */
  pixels?: LightPixelFramePixel[];
}

/** App 连接时向插件注册的设备信息 */
export interface DeviceRegisterParams {
  /** App 侧的稳定设备标识符（重连后不变） */
  deviceId: string;
  platform: "ios" | "android";
  /** APNs device token，仅 iOS 提供，用于 WSS 不可用时兜底推送 */
  apnsToken?: string;
  /** APNs Topic（App Bundle ID），如 "ai.openclaw.ios"，缺省时使用默认值 */
  apnsTopic?: string;
  /** APNs 环境，缺省为 production */
  apnsEnvironment?: "sandbox" | "production";
}

/** 插件内存中维护的设备会话 */
export interface DeviceSession {
  deviceId: string;
  platform: "ios" | "android";
  apnsToken?: string;
  apnsTopic?: string;
  apnsEnvironment?: "sandbox" | "production";
}

// ─── 录音相关类型（arc-long-recording） ───

/** 录音传输状态（约束 R-3：不可跳跃） */
export type RecordingTransferStatus =
  | "receiving_failed"
  | "receiving"
  | "pending_oss_upload"
  | "uploading_oss"
  | "oss_uploaded"
  | "syncing_openclaw"
  | "sync_failed"
  | "synced"
  | "transcribing"
  | "transcribe_failed"
  | "transcribed";

/** 录音关键点标记（约束 R-5：上限 100 个） */
export interface RecordingMarker {
  index: number;
  timestamp_ms: number;
}

/** 录音文件元数据协议（§9） */
export interface RecordingMetadata {
  /** 录音名称，如"与王总沟通产品方向" */
  name: string;
  /** 录音时长（秒），最大 3600 */
  duration_sec: number;
  /** 文件大小（字节） */
  file_size_bytes: number;
  /** 录音创建时间 ISO 8601 */
  created_at: string;
  /** GPS 位置 */
  location?: {
    latitude: number;
    longitude: number;
  };
  /** OSS 音频文件 URL */
  oss_audio_url: string;
  /** OSS 打点文件 URL（可选） */
  oss_srt_url?: string;
  /** 关键点列表 */
  markers: RecordingMarker[];
  /** 传输状态 */
  transfer_status: RecordingTransferStatus;
}

/** Gateway Native 模式：recordings.sync 方法的参数 */
export interface RecordingSyncParams {
  recordingId: string;
  recording: RecordingMetadata;
  /** 客户端下发的本次转写配置；缺省则仅同步录音，不自动转写 */
  asr?: AsrConfig;
}

/** HTTP 备选模式：POST /recordings 的请求体 */
export interface HttpRecordingBody {
  recordingId: string;
  recording: RecordingMetadata;
  /** 客户端下发的本次转写配置；缺省则仅同步录音，不自动转写 */
  asr?: AsrConfig;
}

/** recordings.retranscribe 参数 */
export interface RecordingRetranscribeParams {
  recordingId: string;
  asr: AsrConfig;
}

/** recordings.list 参数 */
export interface RecordingListParams {
  status?: RecordingTransferStatus;
}

/** recordings.status 参数 */
export interface RecordingStatusParams {
  recordingId: string;
  /** 已废弃；recordings.status 现在默认返回结构化转写正文 */
  includeTranscriptContent?: boolean;
}

export interface RecordingTranscriptItem {
  content: string;
  speakerId?: number;
  startTime?: number;
  endTime?: number;
}

/** recordings.rename 参数 */
export interface RecordingRenameParams {
  recordingId: string;
  name: string;
}

/** recordings.asr.init 参数 */
export interface RecordingAsrInitParams {
  asr: AsrConfig;
}

/** 录音列表项 */
export interface RecordingListItem {
  recordingId: string;
  name: string;
  duration_sec: number;
  file_size_bytes: number;
  created_at: string;
  transfer_status: RecordingTransferStatus;
  marker_count: number;
  has_audio: boolean;
  has_srt: boolean;
  has_transcript: boolean;
  audioFile?: string;
  transcriptDataFile?: string;
  transcriptFile?: string;
  updatedAt: string;
  error?: string;
}

/** 单条录音详情 */
export interface RecordingDetail {
  recordingId: string;
  name: string;
  duration_sec: number;
  file_size_bytes: number;
  created_at: string;
  location?: RecordingMetadata["location"];
  oss_audio_url: string;
  oss_srt_url?: string;
  markers: RecordingMarker[];
  transfer_status: RecordingTransferStatus;
  audioFile?: string;
  srtFile?: string;
  transcriptDataFile?: string;
  transcriptFile?: string;
  summaryFile?: string;
  title: string;
  summary?: string;
  transcript?: RecordingTranscriptItem[];
  transcriptData?: unknown;
  ingestedAt: string;
  updatedAt: string;
  error?: string;
}

/** recordings.sync 处理结果 */
export interface RecordingSyncResult {
  ok: boolean;
  /** 客户端传入的录音 ID */
  recordingId: string;
  /** 当前传输状态 */
  transfer_status: RecordingTransferStatus;
  /** 错误信息 */
  error?: string;
}

/** ASR 服务配置（§6.2 两层结构：mode 分层 + 各模式独立配置） */
export interface AsrConfig {
  /** 接入方式（§6.2）：api = 云端 model-proxy 长录音 ASR，local = 本地 Whisper 转写，yoooclaw = YoooClaw 托管（P2 预留） */
  mode: "api" | "local" | "yoooclaw";
  /** 云端 ASR 配置（mode = "api" 时使用，§6.3） */
  api?: AsrApiConfig;
  /** 本地 Whisper 转写配置（mode = "local" 时使用，§6.4） */
  local?: WhisperLocalConfig;
}

/** 云端 ASR 配置（经 model-proxy 长录音接口调用，§6.3） */
export interface AsrApiConfig {
  /** 历史兼容字段：旧客户端会传第三方 provider；当前已忽略，统一走 model-proxy */
  provider?: "dashscope" | "openai" | "model-proxy";
  /** 可选请求级 API Key；传入时优先用于本次 model-proxy 调用，否则回退插件本地 credentials.json */
  apiKey?: string;
  /** 自定义 model-proxy submit-task 端点（可选；留空走当前环境默认地址） */
  endpoint?: string;
  /** 模型名称（兼容保留；当前长录音接口暂未使用） */
  model?: string;
  /** 语言提示（可选，如 "zh"、"en"、"auto"） */
  language?: string;
  /** 是否启用 ITN / 文本规范化，默认 false */
  enableNormalization?: boolean;
  /** 兼容保留；当前长录音接口暂未使用 */
  userId?: string;
  /** 兼容保留；当前长录音接口暂未使用 */
  deviceId?: string;
  /** 兼容保留；当前长录音接口暂未使用 */
  appId?: string;
  /** 兼容保留；当前长录音接口暂未使用 */
  requestId?: string;
  /** 兼容保留；当前长录音接口暂未使用 */
  traceParent?: string;
}

// ─── 本地 Whisper 转写类型（arc-long-recording §6.4） ───

/** Whisper 模型规格（§6.4.3） */
export type WhisperModelSize = "tiny" | "base" | "small" | "medium" | "large-v3";

/** Whisper 推理后端（§6.4.2） */
export type WhisperBackend = "coreml" | "cuda" | "cpu";

/** 模型下载源（§6.4.5） */
export type WhisperModelSource = "auto" | "huggingface" | "domestic";

/** 本地 Whisper 转写配置（§6.4.4） */
export interface WhisperLocalConfig {
  /** 模型规格，留空则根据可用内存自动选择 */
  model?: WhisperModelSize;
  /** 语言提示，默认 "auto"（自动检测） */
  language?: string;
  /** 推理后端，留空则自动检测运行环境 */
  backend?: WhisperBackend;
  /** CPU 推理线程数，默认系统物理核心数 */
  threads?: number;
  /** 是否同时翻译为英文 */
  translate?: boolean;
  /** 模型下载源：auto = 自动探测（尝试 HF → 回退国内镜像），huggingface = 海外 HF，domestic = 国内镜像 */
  modelSource?: WhisperModelSource;
  /** 自定义模型下载 URL（覆盖 modelSource，拼接 /{filename}） */
  modelMirrorUrl?: string;
}

/** 运行环境检测结果 */
export interface WhisperEnvironmentInfo {
  /** 操作系统 */
  os: "darwin" | "linux" | "win32";
  /** CPU 架构 */
  arch: "arm64" | "x64";
  /** 是否 Apple Silicon */
  isAppleSilicon: boolean;
  /** 是否检测到 CUDA */
  hasCuda: boolean;
  /** 推荐的推理后端 */
  recommendedBackend: WhisperBackend;
  /** 可用内存 GB */
  availableMemoryGB: number;
  /** 推荐的模型规格 */
  recommendedModel: WhisperModelSize;
  /** 物理 CPU 核心数 */
  physicalCores: number;
}

/** recordings.asr.init 返回结果 */
export interface RecordingAsrInitResult {
  ok: boolean;
  mode: AsrConfig["mode"];
  provider?: "model-proxy";
  endpoint?: string;
  model?: string;
  language?: string;
  keyConfigured?: boolean;
  localStatus?: {
    environment: WhisperEnvironmentInfo;
    binaryFound: boolean;
    binaryPath: string | null;
    downloadedModels: WhisperModelSize[];
    requestedModel: WhisperModelSize;
  };
  error?: string;
}

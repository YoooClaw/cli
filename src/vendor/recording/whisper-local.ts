/**
 * 本地 Whisper 转写模块（arc-long-recording §6.4）
 *
 * 插件内置 Whisper 推理能力：
 * 1. 自动检测运行环境与硬件配置（§6.4.1）
 * 2. 选择最优推理后端（§6.4.2）
 * 3. 根据可用内存自动选择模型规格（§6.4.3）
 * 4. 首次使用时下载模型文件（§6.4.5）
 * 5. 通过 whisper.cpp CLI 执行本地离线转写
 */

import { execSync, execFileSync, spawnSync, type SpawnSyncReturns } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  statSync,
  createWriteStream,
  unlinkSync,
  copyFileSync,
  openSync,
  readSync,
  closeSync,
} from "node:fs";
import { join, dirname, basename } from "node:path";
import { pipeline } from "node:stream/promises";
import { Readable } from "node:stream";
import { platform, arch, totalmem, cpus, freemem } from "node:os";
import type {
  WhisperLocalConfig,
  WhisperModelSize,
  WhisperModelSource,
  WhisperBackend,
  WhisperEnvironmentInfo,
} from "../types.js";
import type { Logger } from "../logger.js";
import type { TranscriptionResult, TranscriptSegment } from "./asr.js";

// ─── Constants ───

const WHISPER_MODELS_DIR = "whisper-models";
const WHISPER_BIN_DIR = "whisper-bin";

/** Hugging Face GGML 模型下载 URL 模板（海外） */
const HF_MODEL_URL_TEMPLATE =
  "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-{model}.bin";

/** 国内 ModelScope 镜像 URL 模板（lihuoo/whisper.cpp-asr-model-collection） */
const MODELSCOPE_MODEL_URL_TEMPLATE =
  "https://modelscope.cn/models/lihuoo/whisper.cpp-asr-model-collection/resolve/master/{model}.bin";

/** 连通性探测超时（ms） */
const PROBE_TIMEOUT_MS = 5_000;

/** 模型规格 → 文件名映射 */
const MODEL_FILENAMES: Record<WhisperModelSize, string> = {
  tiny: "ggml-tiny.bin",
  base: "ggml-base.bin",
  small: "ggml-small.bin",
  medium: "ggml-medium.bin",
  "large-v3": "ggml-large-v3.bin",
};

/** 模型规格 → 推理内存需求 GB（§6.4.3） */
const MODEL_MEMORY_REQUIREMENTS: Record<WhisperModelSize, number> = {
  tiny: 1,
  base: 1.5,
  small: 3,
  medium: 6,
  "large-v3": 11,
};

/** 模型规格 → 磁盘占用（字节，近似值） */
const MODEL_DISK_SIZES: Record<WhisperModelSize, number> = {
  tiny: 75 * 1024 * 1024,
  base: 140 * 1024 * 1024,
  small: 460 * 1024 * 1024,
  medium: 1500 * 1024 * 1024,
  "large-v3": 3000 * 1024 * 1024,
};

/** whisper.cpp JSON 输出格式 */
interface WhisperCppJsonOutput {
  transcription?: Array<{
    timestamps?: { from: string; to: string };
    offsets?: { from: number; to: number };
    text: string;
  }>;
}

// ─── Public API ───

/**
 * 检测运行环境与硬件配置（§6.4.1）
 *
 * 检测操作系统、CPU 架构、GPU、可用内存，
 * 推荐最优推理后端与模型规格。
 */
export function detectEnvironment(logger: Logger): WhisperEnvironmentInfo {
  const os = platform() as "darwin" | "linux" | "win32";
  const cpuArch = arch() as "arm64" | "x64";
  const isAppleSilicon = os === "darwin" && cpuArch === "arm64";
  const hasCuda = detectCuda(logger);

  // 推荐后端（§6.4.2）
  let recommendedBackend: WhisperBackend;
  if (isAppleSilicon) {
    recommendedBackend = "coreml";
  } else if (hasCuda) {
    recommendedBackend = "cuda";
  } else {
    recommendedBackend = "cpu";
  }

  // 可用内存
  const availableMemoryGB = getAvailableMemoryGB(recommendedBackend, logger);
  const physicalCores = getPhysicalCoreCount();

  // 推荐模型
  const recommendedModel = selectModelByMemory(availableMemoryGB, isAppleSilicon);

  const info: WhisperEnvironmentInfo = {
    os,
    arch: cpuArch,
    isAppleSilicon,
    hasCuda,
    recommendedBackend,
    availableMemoryGB: Math.round(availableMemoryGB * 10) / 10,
    recommendedModel,
    physicalCores,
  };

  logger.info(
    `[whisper-local] 环境检测完成: os=${os}, arch=${cpuArch}, ` +
    `appleSilicon=${isAppleSilicon}, cuda=${hasCuda}, ` +
    `backend=${recommendedBackend}, memory=${info.availableMemoryGB}GB, ` +
    `model=${recommendedModel}, cores=${physicalCores}`,
  );

  return info;
}

/**
 * 根据可用内存自动选择模型规格（§6.4.3）
 *
 * 策略：优先匹配可用内存最大能承载的模型（向下取最近档位）。
 * Apple Silicon CoreML 后端内存效率更高，同档位需求降低 ~30%。
 */
export function selectModelByMemory(
  availableGB: number,
  isAppleSilicon: boolean = false,
): WhisperModelSize {
  // Apple Silicon CoreML 内存效率提升约 30%
  const effectiveGB = isAppleSilicon ? availableGB / 0.7 : availableGB;

  if (effectiveGB >= 12) return "large-v3";
  if (effectiveGB >= 8) return "medium";
  if (effectiveGB >= 6) return "small";
  if (effectiveGB >= 4) return "base";
  return "tiny";
}

/**
 * 解析模型存储目录
 */
export function resolveModelsDir(dataDir: string): string {
  const dir = join(dataDir, WHISPER_MODELS_DIR);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/**
 * 解析 whisper.cpp 二进制目录
 */
export function resolveBinDir(dataDir: string): string {
  const dir = join(dataDir, WHISPER_BIN_DIR);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/**
 * 检查模型文件是否已下载（§6.4.5）
 */
export function isModelDownloaded(modelsDir: string, modelSize: WhisperModelSize): boolean {
  const modelPath = join(modelsDir, MODEL_FILENAMES[modelSize]);
  if (!existsSync(modelPath)) return false;

  // 简单校验：文件大小至少为预期大小的 80%（防止下载不完整）
  try {
    const stat = statSync(modelPath);
    const expectedSize = MODEL_DISK_SIZES[modelSize];
    return stat.size >= expectedSize * 0.8;
  } catch {
    return false;
  }
}

/**
 * 下载 Whisper 模型文件（§6.4.5）
 *
 * 下载源选择策略：
 * - modelMirrorUrl 存在 → 使用自定义 URL
 * - modelSource = "huggingface" → 直接使用 Hugging Face
 * - modelSource = "domestic" → 直接使用国内镜像（ModelScope）
 * - modelSource = "auto"（默认）→ 先探测 Hugging Face 连通性（5s 超时），
 *   可达则用 HF，不可达则回退国内镜像
 */
export async function downloadModel(
  modelsDir: string,
  modelSize: WhisperModelSize,
  logger: Logger,
  modelSource?: WhisperModelSource,
  mirrorUrl?: string,
): Promise<{ ok: boolean; modelPath: string; error?: string }> {
  const filename = MODEL_FILENAMES[modelSize];
  const modelPath = join(modelsDir, filename);

  // 已存在且完整 → 跳过
  if (isModelDownloaded(modelsDir, modelSize)) {
    logger.info(`[whisper-local] 模型已存在，跳过下载: ${filename}`);
    return { ok: true, modelPath };
  }

  // 解析下载 URL
  const url = await resolveModelUrl(modelSize, modelSource, mirrorUrl, logger);

  logger.info(
    `[whisper-local] 开始下载模型: ${modelSize} (${formatBytes(MODEL_DISK_SIZES[modelSize])})`,
  );
  logger.info(`[whisper-local] 下载 URL: ${url}`);

  const result = await downloadFromUrl(url, modelPath, logger);

  // auto 模式且 HF 失败 → 尝试国内镜像回退
  if (
    !result.ok &&
    !mirrorUrl &&
    (modelSource === "auto" || !modelSource) &&
    url.includes("huggingface.co")
  ) {
    const fallbackUrl = MODELSCOPE_MODEL_URL_TEMPLATE.replace("{model}", modelSize);
    logger.info(
      `[whisper-local] Hugging Face 下载失败，回退至国内镜像（ModelScope）: ${fallbackUrl}`,
    );
    return downloadFromUrl(fallbackUrl, modelPath, logger);
  }

  return result;
}

/**
 * 解析模型下载 URL
 */
async function resolveModelUrl(
  modelSize: WhisperModelSize,
  modelSource: WhisperModelSource | undefined,
  mirrorUrl: string | undefined,
  logger: Logger,
): Promise<string> {
  // 自定义 URL 优先级最高
  if (mirrorUrl) {
    return `${mirrorUrl.replace(/\/$/, "")}/${MODEL_FILENAMES[modelSize]}`;
  }

  const source = modelSource ?? "auto";

  switch (source) {
    case "huggingface":
      return HF_MODEL_URL_TEMPLATE.replace("{model}", modelSize);
    case "domestic":
      return MODELSCOPE_MODEL_URL_TEMPLATE.replace("{model}", modelSize);
    case "auto": {
      // 探测 Hugging Face 连通性
      const hfReachable = await probeUrl("https://huggingface.co", logger);
      if (hfReachable) {
        logger.info("[whisper-local] Hugging Face 可达，使用海外源");
        return HF_MODEL_URL_TEMPLATE.replace("{model}", modelSize);
      }
      logger.info("[whisper-local] Hugging Face 不可达，使用国内镜像（ModelScope）");
      return MODELSCOPE_MODEL_URL_TEMPLATE.replace("{model}", modelSize);
    }
    default:
      return HF_MODEL_URL_TEMPLATE.replace("{model}", modelSize);
  }
}

/**
 * 探测 URL 连通性（HEAD 请求，短超时）
 */
async function probeUrl(url: string, logger: Logger): Promise<boolean> {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
    try {
      const res = await fetch(url, {
        method: "HEAD",
        signal: controller.signal,
      });
      return res.ok || res.status === 301 || res.status === 302;
    } finally {
      clearTimeout(timer);
    }
  } catch (err: any) {
    logger.info(
      `[whisper-local] 连通性探测失败: ${url} (${err?.name ?? err?.message ?? "unknown"})`,
    );
    return false;
  }
}

/**
 * 从指定 URL 下载模型文件至本地
 */
async function downloadFromUrl(
  url: string,
  modelPath: string,
  logger: Logger,
): Promise<{ ok: boolean; modelPath: string; error?: string }> {
  const tmpPath = `${modelPath}.downloading`;

  try {
    mkdirSync(dirname(modelPath), { recursive: true });

    const controller = new AbortController();
    // 模型下载超时：30 分钟（large-v3 约 3GB）
    const timer = setTimeout(() => controller.abort(), 30 * 60 * 1000);

    try {
      const res = await fetch(url, { signal: controller.signal });

      if (!res.ok) {
        return {
          ok: false,
          modelPath,
          error: `模型下载失败: HTTP ${res.status} ${res.statusText}`,
        };
      }

      if (!res.body) {
        return { ok: false, modelPath, error: "模型下载失败: 响应体为空" };
      }

      const contentLength = Number(res.headers.get("content-length") ?? 0);
      const writeStream = createWriteStream(tmpPath);
      const readable = Readable.fromWeb(res.body as any);

      // 下载进度日志
      let downloaded = 0;
      let lastLogPercent = 0;
      readable.on("data", (chunk: Buffer) => {
        downloaded += chunk.length;
        if (contentLength > 0) {
          const percent = Math.floor((downloaded / contentLength) * 100);
          if (percent >= lastLogPercent + 10) {
            logger.info(
              `[whisper-local] 下载进度: ${percent}% (${formatBytes(downloaded)} / ${formatBytes(contentLength)})`,
            );
            lastLogPercent = percent;
          }
        }
      });

      await pipeline(readable, writeStream);
    } finally {
      clearTimeout(timer);
    }

    // 重命名为正式文件
    const { renameSync } = await import("node:fs");
    renameSync(tmpPath, modelPath);

    const fileSize = statSync(modelPath).size;
    logger.info(
      `[whisper-local] 模型下载完成: ${basename(modelPath)} (${formatBytes(fileSize)})`,
    );

    return { ok: true, modelPath };
  } catch (err: any) {
    // 清理临时文件
    try {
      if (existsSync(tmpPath)) unlinkSync(tmpPath);
    } catch { /* ignore */ }

    const msg = err?.name === "AbortError"
      ? "模型下载超时（30 分钟）"
      : (err?.message ?? String(err));
    logger.error(`[whisper-local] 模型下载失败: ${msg}`);
    return { ok: false, modelPath, error: msg };
  }
}

/**
 * 查找 whisper.cpp CLI 二进制文件
 *
 * 按以下优先级查找：
 * 1. 插件数据目录下的 whisper-bin/
 * 2. 系统 PATH 中的 whisper-cli / whisper-cpp / whisper
 */
export function findWhisperBinary(dataDir: string, logger: Logger): string | null {
  // 1. 检查插件 bin 目录
  const binDir = resolveBinDir(dataDir);
  const binNames = platform() === "win32"
    ? ["whisper-cli.exe", "whisper.exe", "main.exe"]
    : ["whisper-cli", "whisper", "main"];

  for (const name of binNames) {
    const binPath = join(binDir, name);
    if (existsSync(binPath)) {
      logger.info(`[whisper-local] 找到本地二进制: ${binPath}`);
      return binPath;
    }
  }

  // 2. 检查系统 PATH
  const pathNames = platform() === "win32"
    ? ["whisper-cli", "whisper-cpp", "whisper"]
    : ["whisper-cli", "whisper-cpp", "whisper"];

  for (const name of pathNames) {
    try {
      const cmd = platform() === "win32" ? "where" : "which";
      const result = execSync(`${cmd} ${name}`, { encoding: "utf-8", stdio: "pipe" }).trim();
      if (result) {
        logger.info(`[whisper-local] 找到系统 PATH 二进制: ${result}`);
        return result.split("\n")[0].trim();
      }
    } catch {
      // not found, continue
    }
  }

  logger.warn("[whisper-local] 未找到 whisper.cpp 二进制文件");
  return null;
}

/**
 * 使用本地 Whisper 执行转写（§6.4）
 *
 * @param audioFilePath 本地音频文件绝对路径
 * @param localConfig 本地 Whisper 配置
 * @param dataDir 插件数据目录（存放模型和二进制）
 * @param logger 日志
 */
export async function transcribeWithWhisperLocal(
  audioFilePath: string,
  localConfig: WhisperLocalConfig,
  dataDir: string,
  logger: Logger,
): Promise<TranscriptionResult> {
  // 1. 检测环境
  const env = detectEnvironment(logger);

  // 2. 确定后端、模型、线程数
  const backend = localConfig.backend ?? env.recommendedBackend;
  const modelSize = localConfig.model ?? env.recommendedModel;
  const threads = localConfig.threads ?? env.physicalCores;
  const language = localConfig.language ?? "auto";
  const translate = localConfig.translate ?? false;

  logger.info(
    `[whisper-local] 转写参数: backend=${backend}, model=${modelSize}, ` +
    `threads=${threads}, language=${language}, translate=${translate}`,
  );

  // 3. 查找 whisper.cpp 二进制
  const whisperBin = findWhisperBinary(dataDir, logger);
  if (!whisperBin) {
    return {
      ok: false,
      error:
        "未找到 whisper.cpp 二进制文件。请安装 whisper.cpp 并确保 whisper-cli 在 PATH 中，" +
        `或将二进制文件放入 ${resolveBinDir(dataDir)}`,
    };
  }

  // 4. 确保模型已下载
  const modelsDir = resolveModelsDir(dataDir);
  if (!isModelDownloaded(modelsDir, modelSize)) {
    logger.info(`[whisper-local] 模型 ${modelSize} 未下载，开始下载...`);
    const downloadResult = await downloadModel(
      modelsDir,
      modelSize,
      logger,
      localConfig.modelSource,
      localConfig.modelMirrorUrl,
    );
    if (!downloadResult.ok) {
      return { ok: false, error: `模型下载失败: ${downloadResult.error}` };
    }
  }

  const modelPath = join(modelsDir, MODEL_FILENAMES[modelSize]);

  // 5. 音频格式转换（whisper.cpp 仅支持 16kHz mono WAV）
  //    通过 magic bytes 判断真实格式，不依赖扩展名
  let inputPath = audioFilePath;
  let tmpWavPath: string | null = null;

  const actualFmt = detectAudioFormat(audioFilePath);
  if (actualFmt !== ".wav") {
    tmpWavPath = audioFilePath.replace(/\.[^.]+$/, ".whisper.wav");
    logger.info(
      `[whisper-local] 转换音频格式: ${audioFilePath} (${actualFmt ?? "未知"}) → WAV (16kHz mono)`,
    );

    const convertResult = convertToWav(audioFilePath, tmpWavPath, actualFmt, logger);
    if (!convertResult.ok) {
      return { ok: false, error: convertResult.error! };
    }
    inputPath = tmpWavPath;
  }

  // 6. 构建 whisper.cpp CLI 参数
  const args = buildWhisperArgs({
    audioFilePath: inputPath,
    modelPath,
    language,
    translate,
    threads,
    backend,
  });

  logger.info(`[whisper-local] 执行: ${whisperBin} ${args.join(" ")}`);

  // 7. 执行转写
  try {
    const startMs = Date.now();
    let result: SpawnSyncReturns<string>;

    try {
      result = spawnSync(whisperBin, args, {
        encoding: "utf-8" as const,
        timeout: 60 * 60 * 1000, // 1 小时超时（5 小时录音）
        maxBuffer: 100 * 1024 * 1024, // 100MB stdout buffer
        stdio: ["pipe", "pipe", "pipe"],
      });
    } catch (err: any) {
      cleanupTmpWav(tmpWavPath);
      return { ok: false, error: `whisper.cpp 执行失败: ${err?.message ?? err}` };
    }

    if (result.status !== 0) {
      cleanupTmpWav(tmpWavPath);
      const stderr = result.stderr?.slice(0, 500) ?? "";
      return {
        ok: false,
        error: `whisper.cpp 退出码 ${result.status}: ${stderr}`,
      };
    }

    const elapsed = Date.now() - startMs;
    logger.info(`[whisper-local] 转写耗时: ${Math.round(elapsed / 1000)}s`);

    // 8. 读取 JSON 输出文件（--output-json 将结果写入 {inputPath}.json）
    const jsonPath = inputPath + ".json";
    let jsonContent: string;

    if (existsSync(jsonPath)) {
      jsonContent = readFileSync(jsonPath, "utf-8");
      // 清理 JSON 文件
      try { unlinkSync(jsonPath); } catch { /* ignore */ }
    } else {
      // 回退：尝试解析 stdout（部分 whisper.cpp 版本可能输出到 stdout）
      jsonContent = result.stdout;
    }

    cleanupTmpWav(tmpWavPath);

    return parseWhisperOutput(jsonContent, logger);
  } catch (err: any) {
    cleanupTmpWav(tmpWavPath);
    return { ok: false, error: `whisper.cpp 转写异常: ${err?.message ?? err}` };
  }
}

/**
 * 获取本地 Whisper 状态信息（供 App 查询环境检测结果）
 */
export function getWhisperLocalStatus(
  dataDir: string,
  logger: Logger,
): {
  environment: WhisperEnvironmentInfo;
  binaryFound: boolean;
  binaryPath: string | null;
  downloadedModels: WhisperModelSize[];
} {
  const env = detectEnvironment(logger);
  const binaryPath = findWhisperBinary(dataDir, logger);
  const modelsDir = resolveModelsDir(dataDir);

  const allModels: WhisperModelSize[] = ["tiny", "base", "small", "medium", "large-v3"];
  const downloadedModels = allModels.filter((m) => isModelDownloaded(modelsDir, m));

  return {
    environment: env,
    binaryFound: binaryPath !== null,
    binaryPath,
    downloadedModels,
  };
}

// ─── Internal Helpers ───

/**
 * 检测 CUDA 是否可用（§6.4.1）
 */
function detectCuda(logger: Logger): boolean {
  try {
    // 尝试运行 nvidia-smi
    execSync("nvidia-smi", { encoding: "utf-8", stdio: "pipe", timeout: 5000 });
    logger.info("[whisper-local] 检测到 NVIDIA GPU (nvidia-smi)");
    return true;
  } catch {
    // nvidia-smi 不存在或执行失败
  }

  // 检查 CUDA runtime 库
  if (platform() === "linux") {
    try {
      const ldconfig = execSync("ldconfig -p 2>/dev/null | grep libcudart", {
        encoding: "utf-8",
        stdio: "pipe",
        timeout: 5000,
      });
      if (ldconfig.includes("libcudart")) {
        logger.info("[whisper-local] 检测到 CUDA runtime (ldconfig)");
        return true;
      }
    } catch { /* ignore */ }
  }

  return false;
}

/**
 * 获取可用内存（GB）
 *
 * CUDA 后端使用 VRAM，其余使用系统 RAM。
 */
function getAvailableMemoryGB(backend: WhisperBackend, logger: Logger): number {
  if (backend === "cuda") {
    // 尝试获取 GPU VRAM
    try {
      const output = execSync(
        "nvidia-smi --query-gpu=memory.free --format=csv,noheader,nounits",
        { encoding: "utf-8", stdio: "pipe", timeout: 5000 },
      );
      const freeVramMB = parseInt(output.trim().split("\n")[0], 10);
      if (!Number.isNaN(freeVramMB) && freeVramMB > 0) {
        return freeVramMB / 1024;
      }
    } catch {
      logger.warn("[whisper-local] 无法获取 GPU VRAM，使用系统 RAM 代替");
    }
  }

  // 使用系统可用内存（取 total 的 80% 和 free 的较大值作为可用估计）
  const totalGB = totalmem() / (1024 * 1024 * 1024);
  const freeGB = freemem() / (1024 * 1024 * 1024);
  return Math.max(freeGB, totalGB * 0.6);
}

/**
 * 获取物理 CPU 核心数
 */
function getPhysicalCoreCount(): number {
  const cpuList = cpus();
  if (cpuList.length === 0) return 4; // fallback

  // 简单估算：逻辑核 / 2（假设超线程）
  // macOS ARM 无超线程，直接使用逻辑核数
  if (platform() === "darwin" && arch() === "arm64") {
    // Apple Silicon: P-cores only (exclude E-cores for inference)
    // 简化处理：使用总核数
    return cpuList.length;
  }

  return Math.max(1, Math.floor(cpuList.length / 2));
}

/**
 * 通过文件头 magic bytes 检测音频的真实格式
 * @returns 正确的文件扩展名（如 .aiff, .wav, .m4a），或 null
 */
function detectAudioFormat(filePath: string): string | null {
  try {
    const fd = openSync(filePath, "r");
    const buf = Buffer.alloc(12);
    readSync(fd, buf, 0, 12, 0);
    closeSync(fd);

    const header = buf.toString("ascii", 0, 4);
    const header8 = buf.toString("ascii", 0, 8);

    // RIFF....WAVE = WAV
    if (header === "RIFF" && buf.toString("ascii", 8, 12) === "WAVE") return ".wav";
    // FORM....AIFF / FORM....AIFC = AIFF
    if (header === "FORM") {
      const sub = buf.toString("ascii", 8, 12);
      if (sub === "AIFF" || sub === "AIFC") return ".aiff";
    }
    // ftyp = MP4/M4A/AAC container
    if (buf.toString("ascii", 4, 8) === "ftyp") return ".m4a";
    // ID3 or 0xFF 0xFB = MP3
    if (header.startsWith("ID3") || (buf[0] === 0xff && (buf[1] & 0xe0) === 0xe0)) return ".mp3";
    // OggS = Ogg (Vorbis/Opus)
    if (header === "OggS") return ".ogg";
    // fLaC = FLAC
    if (header === "fLaC") return ".flac";

    return null;
  } catch {
    return null;
  }
}

/**
 * 将音频文件转换为 whisper.cpp 所需的 16kHz mono WAV 格式
 *
 * 转换链（按优先级）：
 * 1. ffmpeg — 支持全格式，含 OGG/Opus
 * 2. opusdec — 专用于 OGG/Opus（来自 brew install opus-tools）
 * 3. afconvert — macOS 内置，支持 AIFF/M4A/AAC；需正确文件扩展名
 *
 * @param actualFmt detectAudioFormat 检测到的真实格式（如 ".ogg", ".aiff"），可为 null
 */
function convertToWav(
  inputPath: string,
  outputPath: string,
  actualFmt: string | null,
  logger: Logger,
): { ok: boolean; error?: string } {
  // 尝试 ffmpeg（可自动检测格式，不依赖扩展名）
  try {
    const ffmpegResult = spawnSync("ffmpeg", [
      "-y", "-i", inputPath,
      "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
      outputPath,
    ], {
      encoding: "utf-8",
      timeout: 120_000,
      stdio: ["pipe", "pipe", "pipe"],
    });

    if (ffmpegResult.status === 0 && existsSync(outputPath)) {
      logger.info(`[whisper-local] ffmpeg 转换完成: ${outputPath}`);
      return { ok: true };
    }
  } catch {
    // ffmpeg 不可用，继续尝试其他方式
  }

  // 2. opusdec — 专用于 OGG/Opus（ffmpeg 不可用时）
  if (actualFmt === ".ogg") {
    try {
      const opusResult = spawnSync(
        "opusdec",
        ["--rate", "16000", "--mono", inputPath, outputPath],
        { encoding: "utf-8", timeout: 120_000, stdio: ["pipe", "pipe", "pipe"] },
      );
      if (opusResult.status === 0 && existsSync(outputPath)) {
        logger.info(`[whisper-local] opusdec 转换完成: ${outputPath}`);
        return { ok: true };
      }
    } catch { /* opusdec 不可用，继续 */ }
  }

  // 3. macOS afconvert — 支持 AIFF/M4A/AAC，不支持 OGG
  if (process.platform === "darwin" && actualFmt !== ".ogg") {
    // afconvert 依赖扩展名判断输入格式，先确保扩展名正确
    let actualInputPath = inputPath;
    let tmpCopy: string | null = null;
    const detectedExt = actualFmt;

    if (detectedExt && !inputPath.endsWith(detectedExt)) {
      // 扩展名不匹配，创建带正确扩展名的临时副本
      tmpCopy = inputPath + ".detected" + detectedExt;
      try {
        copyFileSync(inputPath, tmpCopy);
        actualInputPath = tmpCopy;
        logger.info(
          `[whisper-local] 检测到实际格式 ${detectedExt}，临时重命名`,
        );
      } catch {
        tmpCopy = null;
      }
    }

    try {
      const afResult = spawnSync("afconvert", [
        "-f", "WAVE", "-d", "LEI16@16000", "-c", "1",
        actualInputPath, outputPath,
      ], {
        encoding: "utf-8",
        timeout: 120_000,
        stdio: ["pipe", "pipe", "pipe"],
      });

      // 清理临时副本
      if (tmpCopy && existsSync(tmpCopy)) {
        try { unlinkSync(tmpCopy); } catch { /* ignore */ }
      }

      if (afResult.status === 0 && existsSync(outputPath)) {
        logger.info(`[whisper-local] afconvert 转换完成: ${outputPath}`);
        return { ok: true };
      }

      const stderr = afResult.stderr?.slice(0, 200) ?? "";
      return { ok: false, error: `afconvert 转换失败 (exit ${afResult.status}): ${stderr}` };
    } catch (err: any) {
      // 清理临时副本
      if (tmpCopy && existsSync(tmpCopy)) {
        try { unlinkSync(tmpCopy); } catch { /* ignore */ }
      }
      return { ok: false, error: `afconvert 不可用: ${err?.message}` };
    }
  }

  const fmtHint = actualFmt === ".ogg"
    ? "OGG/Opus 格式需要 ffmpeg 或 opus-tools（brew install opus-tools）"
    : "请安装 ffmpeg（brew install ffmpeg）或确保音频文件为 WAV 格式";
  return { ok: false, error: `无法将音频转换为 WAV 格式。${fmtHint}` };
}

/** 清理临时 WAV 文件 */
function cleanupTmpWav(path: string | null): void {
  if (path && existsSync(path)) {
    try { unlinkSync(path); } catch { /* ignore */ }
  }
}

/**
 * 构建 whisper.cpp CLI 参数
 */
function buildWhisperArgs(params: {
  audioFilePath: string;
  modelPath: string;
  language: string;
  translate: boolean;
  threads: number;
  backend: WhisperBackend;
}): string[] {
  const args: string[] = [
    "--model", params.modelPath,
    "--file", params.audioFilePath,
    "--output-json",        // JSON 格式输出到 {audioFilePath}.json
    "--no-prints",          // 抑制模型信息输出
    "--threads", String(params.threads),
  ];

  // 语言
  if (params.language && params.language !== "auto") {
    args.push("--language", params.language);
  } else {
    args.push("--language", "auto");
  }

  // 翻译为英文
  if (params.translate) {
    args.push("--translate");
  }

  return args;
}

/**
 * 解析 whisper.cpp JSON 输出
 */
function parseWhisperOutput(
  stdout: string,
  logger: Logger,
): TranscriptionResult {
  if (!stdout || !stdout.trim()) {
    return { ok: false, error: "whisper.cpp 无输出" };
  }

  try {
    const data = JSON.parse(stdout.trim()) as WhisperCppJsonOutput;

    if (!data.transcription || !Array.isArray(data.transcription)) {
      // 可能是纯文本输出
      return {
        ok: true,
        text: stdout.trim(),
        segments: [],
      };
    }

    const segments: TranscriptSegment[] = [];
    const textParts: string[] = [];

    for (const item of data.transcription) {
      const text = item.text?.trim() ?? "";
      if (!text) continue;

      textParts.push(text);

      const startMs = item.offsets?.from ?? parseTimestamp(item.timestamps?.from);
      const endMs = item.offsets?.to ?? parseTimestamp(item.timestamps?.to);

      segments.push({
        start_ms: startMs,
        end_ms: endMs,
        text,
      });
    }

    const fullText = textParts.join(" ");

    logger.info(
      `[whisper-local] 转写完成: ${fullText.length} 字, ${segments.length} 段`,
    );

    return {
      ok: true,
      text: fullText,
      segments,
    };
  } catch (err: any) {
    logger.warn(`[whisper-local] JSON 解析失败，尝试纯文本: ${err?.message}`);

    // Fallback: 将 stdout 视为纯文本
    return {
      ok: true,
      text: stdout.trim(),
      segments: [],
    };
  }
}

/**
 * 解析 whisper.cpp 时间戳字符串 "HH:MM:SS,mmm" → 毫秒
 */
function parseTimestamp(ts?: string): number {
  if (!ts) return 0;
  // "00:00:05,000" or "00:00:05.000"
  const match = ts.match(/^(\d+):(\d+):(\d+)[,.](\d+)$/);
  if (!match) return 0;
  const [, h, m, s, ms] = match;
  return (
    parseInt(h, 10) * 3600000 +
    parseInt(m, 10) * 60000 +
    parseInt(s, 10) * 1000 +
    parseInt(ms, 10)
  );
}

/**
 * 格式化字节数
 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)}MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)}GB`;
}

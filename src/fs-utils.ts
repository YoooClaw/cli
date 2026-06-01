/**
 * 文件系统工具 —— 统一目录/文件权限与原子写。
 *
 * 凭据与配置落盘的安全约束（PRD「非功能需求 / 安全」）：
 *   - 目录 0700、敏感文件 0600；
 *   - 写入走「临时文件 + rename」原子替换，避免半截写。
 */
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { YoooclawError } from "./errors.js";

/** 目录默认权限（私有）。 */
export const DIR_MODE = 0o700;
/** 敏感文件权限（仅属主可读写）。 */
export const SECRET_FILE_MODE = 0o600;
/** 普通配置文件权限。 */
export const CONFIG_FILE_MODE = 0o644;

/** 确保目录存在并尽力收紧权限（Windows 上 chmod 基本 no-op）。 */
export function ensureDir(dir: string, mode: number = DIR_MODE): void {
  mkdirSync(dir, { recursive: true, mode });
  try {
    chmodSync(dir, mode);
  } catch {
    // Windows / 受限文件系统：忽略
  }
}

/** 原子写文件：先写临时文件再 rename，最后 chmod。 */
export function writeFileAtomic(
  filePath: string,
  contents: string,
  mode: number = CONFIG_FILE_MODE,
): void {
  ensureDir(dirname(filePath));
  const tmp = join(
    dirname(filePath),
    `.${Date.now()}-${Math.random().toString(36).slice(2)}.tmp`,
  );
  writeFileSync(tmp, contents, { mode });
  renameSync(tmp, filePath);
  try {
    chmodSync(filePath, mode);
  } catch {
    // 忽略
  }
}

/** 写 JSON（两空格缩进 + 末尾换行）。 */
export function writeJsonFile(
  filePath: string,
  data: unknown,
  mode: number = CONFIG_FILE_MODE,
): void {
  writeFileAtomic(filePath, JSON.stringify(data, null, 2) + "\n", mode);
}

/** 读 JSON；文件不存在返回 undefined；解析失败抛 CONFIG_INVALID。 */
export function readJsonFile<T = unknown>(filePath: string): T | undefined {
  if (!existsSync(filePath)) return undefined;
  let raw: string;
  try {
    raw = readFileSync(filePath, "utf-8");
  } catch (err) {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `读取文件失败：${filePath}`,
      { cause: err instanceof Error ? err.message : String(err) },
    );
  }
  try {
    return JSON.parse(raw) as T;
  } catch {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `文件不是合法 JSON：${filePath}`,
      { hint: "可手动修复或删除后重新生成" },
    );
  }
}

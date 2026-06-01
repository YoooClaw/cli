/**
 * 全局执行上下文 —— 由全局 flags 解析得到，贯穿所有命令。
 */
import { existsSync, readFileSync } from "node:fs";
import {
  DEFAULT_PROFILE,
  activeProfilePath,
  profilePaths,
  type ProfilePaths,
} from "./paths.js";
import { resolveFormat, type OutputFormat } from "./output/format.js";

export interface GlobalFlags {
  profile?: string;
  format?: string;
  quiet?: boolean;
  color?: boolean;
}

export interface CliContext {
  profile: string;
  paths: ProfilePaths;
  format: OutputFormat;
  quiet: boolean;
  color: boolean;
}

/** 解析当前 active profile：--profile > $YOOOCLAW_PROFILE > active-profile 文件 > default。 */
export function resolveActiveProfile(flagProfile?: string): string {
  if (flagProfile?.trim()) return flagProfile.trim();
  const envProfile = process.env.YOOOCLAW_PROFILE?.trim();
  if (envProfile) return envProfile;
  const file = activeProfilePath();
  if (existsSync(file)) {
    const name = readFileSync(file, "utf-8").trim();
    if (name) return name;
  }
  return DEFAULT_PROFILE;
}

/** 把全局 flags 物化成贯穿命令的 CliContext。 */
export function buildContext(flags: GlobalFlags): CliContext {
  const profile = resolveActiveProfile(flags.profile);
  return {
    profile,
    paths: profilePaths(profile),
    format: resolveFormat(flags.format),
    quiet: flags.quiet ?? false,
    // commander 对 --no-color 解析为 color:false；缺省 true
    color: flags.color ?? true,
  };
}

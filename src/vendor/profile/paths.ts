/**
 * 宿主原始路径解析。
 *
 * 注意：这里解析的是宿主暴露的目录，不保证可写。JvsClaw 可能把
 * JVSCLAW_STATE_DIR / JVSCLAW_HOME 指向只读 rootfs（例如 /opt/jvs-claw）。
 * 插件自己的可写目录必须通过 runtime-paths.ts 解析。
 */
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";

interface QClawMeta {
  stateDir?: string;
  configPath?: string;
}

export const DEFAULT_JVSCLAW_STATE_DIR = "/home/admin/.openclaw";
export const PHONE_NOTIFICATIONS_STATE_DIR_ENV = "PHONE_NOTIFICATIONS_STATE_DIR";
export const PHONE_NOTIFICATIONS_CONFIG_PATH_ENV = "PHONE_NOTIFICATIONS_CONFIG_PATH";

export function trimToUndefined(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed || undefined;
}

export function homeDir(): string {
  return process.env.HOME || process.env.USERPROFILE || "/tmp";
}

export function expandUserPath(value: string | undefined): string | undefined {
  if (!value) {
    return undefined;
  }
  if (value === "~") {
    return homeDir();
  }
  if (value.startsWith("~/")) {
    return join(homeDir(), value.slice(2));
  }
  return value;
}

function candidateMetaPaths(): string[] {
  const home = homeDir();
  return [
    join(home, ".qclaw", "qclaw.json"),
    join(home, ".qclow", "qclaw.json"),
  ];
}

export function loadQClawMeta(): QClawMeta | undefined {
  for (const metaPath of candidateMetaPaths()) {
    if (!existsSync(metaPath)) {
      continue;
    }
    try {
      const parsed = JSON.parse(readFileSync(metaPath, "utf-8")) as QClawMeta;
      return {
        stateDir: expandUserPath(trimToUndefined(parsed?.stateDir)),
        configPath: expandUserPath(trimToUndefined(parsed?.configPath)),
      };
    } catch {
      // ignore invalid metadata and keep trying
    }
  }
  return undefined;
}

/** 插件可写状态目录的显式覆盖。与宿主原始 stateDir 分离。 */
export function resolvePluginStateDirOverrideFromEnv(): string | undefined {
  return expandUserPath(
    trimToUndefined(process.env[PHONE_NOTIFICATIONS_STATE_DIR_ENV]),
  );
}

/** 插件使用的配置文件显式覆盖。与宿主原始 configPath 分离。 */
export function resolvePluginConfigPathOverrideFromEnv(): string | undefined {
  return expandUserPath(
    trimToUndefined(process.env[PHONE_NOTIFICATIONS_CONFIG_PATH_ENV]),
  );
}

/** 解析宿主暴露的原始状态目录环境变量。返回值不保证可写。 */
export function resolveStateDirFromEnv(): string | undefined {
  return expandUserPath(
    trimToUndefined(process.env.JVSCLAW_STATE_DIR) ??
      trimToUndefined(process.env.JVSCLAW_HOME) ??
      trimToUndefined(process.env.OPENCLAW_STATE_DIR) ??
      trimToUndefined(process.env.QCLAW_STATE_DIR) ??
      trimToUndefined(process.env.OPENCLAW_ROOT) ??
      trimToUndefined(process.env.OPENCLAW_HOME) ??
      trimToUndefined(process.env.QCLAW_HOME),
  );
}

/** 解析宿主暴露的原始配置文件环境变量。返回值不保证可写。 */
export function resolveConfigPathFromEnv(): string | undefined {
  return expandUserPath(
    trimToUndefined(process.env.JVSCLAW_CONFIG_PATH) ??
      trimToUndefined(process.env.OPENCLAW_CONFIG_PATH) ??
      trimToUndefined(process.env.QCLAW_CONFIG_PATH),
  );
}

export function hasOpenClawMarkers(): boolean {
  const baseDir = join(homeDir(), ".openclaw");
  return [
    join(baseDir, "openclaw.json"),
    join(baseDir, "credentials.json"),
    join(baseDir, "extensions"),
  ].some((candidate) => existsSync(candidate));
}

/**
 * 解析宿主原始状态目录。仅用于必须与宿主运行时交互的场景，例如 Gateway
 * 设备配对。插件文件写入请使用 runtime-paths.ts 的 resolveStateDir()。
 */
export function resolveHostStateDir(stateDir?: string): string {
  const explicitDir = expandUserPath(trimToUndefined(stateDir));
  if (explicitDir) {
    return explicitDir;
  }

  const envDir = resolveStateDirFromEnv();
  if (envDir) {
    return envDir;
  }

  if (hasOpenClawMarkers()) {
    return join(homeDir(), ".openclaw");
  }

  const meta = loadQClawMeta();
  if (meta?.stateDir) {
    return meta.stateDir;
  }
  if (meta?.configPath) {
    return dirname(meta.configPath);
  }

  return join(homeDir(), ".openclaw");
}

/** 解析宿主原始配置文件路径。返回值不保证位于可写目录。 */
export function resolveHostConfigPath(
  stateDir = resolveHostStateDir(),
): string {
  const envConfigPath = resolveConfigPathFromEnv();
  if (envConfigPath) {
    return envConfigPath;
  }

  const meta = loadQClawMeta();
  if (
    meta?.configPath &&
    (!meta.stateDir || !stateDir || meta.stateDir === stateDir)
  ) {
    return meta.configPath;
  }

  return join(stateDir, "openclaw.json");
}

/**
 * 返回可能持久化 PHONE_NOTIFICATIONS_CLAW_PROFILE 的目录。
 * detect.ts 使用这些候选路径判断渠道，避免因宿主只读根目录而漏读 JvsClaw 配置。
 */
export function resolvePersistedProfileStateDirs(): string[] {
  const dirs: string[] = [];
  const add = (dir: string | undefined) => {
    if (dir && !dirs.includes(dir)) {
      dirs.push(dir);
    }
  };

  add(resolvePluginStateDirOverrideFromEnv());
  add(DEFAULT_JVSCLAW_STATE_DIR);
  add(resolveHostStateDir());
  return dirs;
}

/**
 * 插件可写路径解析。
 *
 * 宿主原始目录与插件可写目录在 OpenClaw / QClaw 下通常相同，但 JvsClaw
 * 可能把宿主目录指向只读 rootfs。所有插件文件写入都应经过这里。
 */
import { join } from "node:path";
import { resolveClawKind } from "./detect.js";
import {
  DEFAULT_JVSCLAW_STATE_DIR,
  resolveHostConfigPath,
  resolveHostStateDir,
  resolvePluginConfigPathOverrideFromEnv,
  resolvePluginStateDirOverrideFromEnv,
} from "./paths.js";

/**
 * 解析插件可写状态目录。
 *
 * PHONE_NOTIFICATIONS_STATE_DIR 是明确的可写覆盖；JVSCLAW_STATE_DIR 等变量
 * 只描述宿主原始目录，不参与 JvsClaw 插件文件写入。
 */
export function resolveStateDir(hostStateDir?: string): string {
  const override = resolvePluginStateDirOverrideFromEnv();
  if (override) {
    return override;
  }

  if (resolveClawKind() === "jvsclaw") {
    return DEFAULT_JVSCLAW_STATE_DIR;
  }

  return resolveHostStateDir(hostStateDir);
}

/** 解析插件应使用的配置文件路径。 */
export function resolveConfigPath(hostStateDir?: string): string {
  const override = resolvePluginConfigPathOverrideFromEnv();
  if (override) {
    return override;
  }

  if (resolveClawKind() === "jvsclaw") {
    return join(resolveStateDir(hostStateDir), "openclaw.json");
  }

  return resolveHostConfigPath(resolveHostStateDir(hostStateDir));
}

/** 解析插件可写状态目录中的文件。 */
export function resolveStateFile(
  filename: string,
  hostStateDir?: string,
): string {
  return join(resolveStateDir(hostStateDir), filename);
}

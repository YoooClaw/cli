/**
 * 宿主路径/检测的兼容入口。
 *
 * 实现已迁移到 src/profile/{paths,runtime-paths,detect}.ts。本文件保留原有导出签名，
 * 让现有 import 路径（"../host.js" 等）无需改动，行为完全一致。
 * 新代码请优先从 "./profile/index.js" 引入 resolveClawProfile()。
 */
export type { HostKind } from "./profile/types.js";
export { detectHostKind } from "./profile/detect.js";
export {
  resolveHostConfigPath,
  resolveHostStateDir,
} from "./profile/paths.js";
export {
  resolveStateDir,
  resolveConfigPath,
  resolveStateFile,
} from "./profile/runtime-paths.js";

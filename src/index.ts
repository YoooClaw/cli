/**
 * `@yoooclaw/cli` 程序化入口。
 *
 * - `run(argv)` 解析并执行命令（bin.ts 调用）。
 * - 同时再导出核心模块，供测试与未来的程序化集成使用。
 */
import { buildProgram } from "./program.js";

export async function run(argv: string[] = process.argv): Promise<void> {
  const program = buildProgram();
  await program.parseAsync(argv);
}

export { buildProgram } from "./program.js";
export { COMMAND_TREE } from "./command-tree.js";
export { CLI_VERSION } from "./version.js";
export { ErrorCode, YoooclawError } from "./errors.js";
export {
  renderResult,
  renderError,
  resolveFormat,
  type OutputFormat,
} from "./output/format.js";
export {
  buildContext,
  resolveActiveProfile,
  type CliContext,
  type GlobalFlags,
} from "./context.js";
export * as paths from "./paths.js";

/**
 * 构建 yoooclaw 的 commander 程序。
 *
 * 命令树结构（service / subcommand / shortcut / options）来自 command-tree.ts；
 * 实现体由 commands/registry.ts 按 path（如 `config init`）提供，未注册则回落 notImplemented。
 */
import { Command } from "commander";
import {
  COMMAND_TREE,
  type OptionSpec,
  type ServiceSpec,
} from "./command-tree.js";
import { buildContext, type CliContext, type GlobalFlags } from "./context.js";
import { renderError, renderResult } from "./output/format.js";
import { YoooclawError, notImplemented } from "./errors.js";
import { CLI_VERSION } from "./version.js";
import { HANDLERS } from "./commands/registry.js";

/** 命令处理器签名：拿到上下文 + commander 解析后的参数/选项，返回可序列化结果。 */
export type CommandHandler = (
  ctx: CliContext,
  args: unknown[],
  opts: Record<string, unknown>,
) => unknown;

/** 把 handler 包成 commander action：解析 ctx → 执行 → 统一渲染结果 / 错误。 */
function wrapAction(handler: CommandHandler) {
  return async (...rawArgs: unknown[]): Promise<void> => {
    // commander 传参：[...positional, options, command]
    const command = rawArgs.at(-1) as Command;
    const opts = rawArgs.at(-2) as Record<string, unknown>;
    const positionals = rawArgs.slice(0, -2);
    const globals = command.optsWithGlobals() as GlobalFlags;

    let ctx: CliContext | undefined;
    try {
      ctx = buildContext(globals);
      const result = await handler(ctx, positionals, opts);
      renderResult(result, { format: ctx.format });
    } catch (err) {
      const format = ctx?.format ?? "json";
      renderError(err, { format });
      process.exitCode = err instanceof YoooclawError ? err.exitCode : 1;
    }
  };
}

/** 解析某个 path 的 handler，未注册则给 notImplemented 占位。 */
function resolveHandler(path: string): CommandHandler {
  const handler = HANDLERS[path];
  if (handler) return handler;
  return () => {
    throw notImplemented(path);
  };
}

function applyOptions(cmd: Command, options: OptionSpec[] | undefined): void {
  for (const opt of options ?? []) {
    if (opt.default !== undefined) {
      cmd.option(opt.flags, opt.summary, opt.default);
    } else {
      cmd.option(opt.flags, opt.summary);
    }
  }
}

function attachService(program: Command, spec: ServiceSpec): void {
  const service = program.command(spec.name).description(spec.summary);

  // leaf 命令：直接挂 action（如 `yoooclaw doctor` / `yoooclaw api <method> <path>`）
  if (!spec.subcommands || spec.subcommands.length === 0) {
    if (spec.args) {
      service.arguments(spec.args);
    }
    applyOptions(service, spec.options);
    service.action(wrapAction(resolveHandler(spec.name)));
  } else {
    for (const sub of spec.subcommands) {
      const subCmd = service.command(sub.name).description(sub.summary);
      applyOptions(subCmd, sub.options);
      // path 的注册 key 用「服务名 + 子命令名（去掉位置参数占位）」
      subCmd.action(wrapAction(resolveHandler(handlerKey(spec.name, sub.name))));
    }
  }

  // shortcuts（`+` 前缀）作为该 service 下的子命令注册
  for (const shortcut of spec.shortcuts ?? []) {
    const shortcutCmd = service
      .command(shortcut.name)
      .description(shortcut.summary);
    applyOptions(shortcutCmd, shortcut.options);
    shortcutCmd.action(
      wrapAction(resolveHandler(handlerKey(spec.name, shortcut.name))),
    );
  }
}

/** 注册表的 path key：去掉子命令名里的位置参数占位（`show <id>` → `show`）。 */
export function handlerKey(service: string, subName: string): string {
  const bare = subName.split(/\s+/)[0];
  return `${service} ${bare}`;
}

/** 构建并返回顶层 commander 程序。 */
export function buildProgram(): Command {
  const program = new Command();

  program
    .name("yoooclaw")
    .description(
      "yoooclaw —— 独立守护进程 + CLI：接收手机通知、Relay 隧道、灯效规则评估（Agent-Native）",
    )
    .version(CLI_VERSION, "-v, --version", "显示 CLI 版本号")
    // 全局 flags（对所有命令生效）
    .option("--profile <name>", "切换 profile（默认 default）")
    .option(
      "--format <format>",
      "输出格式：json|pretty|table|ndjson（TTY 默认 pretty，管道默认 json）",
    )
    .option("--quiet", "抑制进度日志，只输出最终结果")
    .option("--no-color", "关闭终端颜色")
    .showHelpAfterError();

  for (const spec of COMMAND_TREE) {
    attachService(program, spec);
  }

  return program;
}

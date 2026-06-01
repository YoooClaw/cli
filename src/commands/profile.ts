/**
 * profile service —— list / use / create / delete。
 */
import { existsSync, rmSync } from "node:fs";
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { writeFileAtomic } from "../fs-utils.js";
import { confirm } from "../prompt.js";
import {
  DEFAULT_PROFILE,
  activeProfilePath,
  listProfileNames,
  profilePaths,
  readActiveProfile,
} from "../paths.js";
import { configExists } from "../config/store.js";
import { configInit } from "./config.js";

export function profileList(ctx: CliContext): unknown {
  const active = readActiveProfile() ?? DEFAULT_PROFILE;
  const names = listProfileNames();
  // active / default 即便目录还没建出来也列出来，避免空列表误导
  for (const implied of [active, DEFAULT_PROFILE]) {
    if (!names.includes(implied)) names.push(implied);
  }
  return names
    .sort((a, b) => a.localeCompare(b))
    .map((name) => ({
      name,
      active: name === active,
      initialized: configExists(profilePaths(name)),
    }));
}

export function profileUse(_ctx: CliContext, args: unknown[]): unknown {
  const [name] = args as [string];
  const paths = profilePaths(name);
  if (!existsSync(paths.dir)) {
    throw new YoooclawError(
      "YOOOCLAW_PROFILE_NOT_FOUND",
      `profile \`${name}\` 不存在`,
      { hint: "用 yoooclaw profile create 新建", checkedPaths: [paths.dir] },
    );
  }
  writeFileAtomic(activeProfilePath(), `${name}\n`);
  return { ok: true, active: name };
}

export async function profileCreate(
  ctx: CliContext,
  args: unknown[],
  opts: Record<string, unknown>,
): Promise<unknown> {
  const [name] = args as [string];
  const paths = profilePaths(name);
  if (configExists(paths)) {
    throw new YoooclawError(
      "YOOOCLAW_ALREADY_EXISTS",
      `profile \`${name}\` 已存在`,
    );
  }
  // 在目标 profile 的上下文里跑 init 向导
  const subCtx: CliContext = { ...ctx, profile: name, paths };
  return configInit(subCtx, [], opts as never);
}

export async function profileDelete(
  ctx: CliContext,
  args: unknown[],
  opts: { yes?: boolean },
): Promise<unknown> {
  const [name] = args as [string];
  const active = readActiveProfile() ?? DEFAULT_PROFILE;
  if (name === active) {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      `不能删除当前 active profile \`${name}\``,
      { hint: "先 yoooclaw profile use <其他 profile> 再删除" },
    );
  }
  const paths = profilePaths(name);
  if (!existsSync(paths.dir)) {
    throw new YoooclawError("YOOOCLAW_PROFILE_NOT_FOUND", `profile \`${name}\` 不存在`);
  }
  if (!opts.yes && !(await confirm(`确认删除 profile \`${name}\` 及其全部数据？`, false))) {
    throw new YoooclawError("YOOOCLAW_CONFIRMATION_REQUIRED", "已取消");
  }
  rmSync(paths.dir, { recursive: true, force: true });
  return { ok: true, deleted: name };
}

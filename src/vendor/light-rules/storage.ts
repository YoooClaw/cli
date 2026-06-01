import {
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  rmSync,
  readdirSync,
  statSync,
} from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import type { LightSegment } from "../types.js";
import type { LightRuleMeta } from "./types.js";
import { assertAncsRepeatTimes, normalizeRepeatTimes } from "../light/repeat.js";

export interface LightRuleStorageContext {
  workspaceDir?: string;
  stateDir?: string;
}

function addUniquePath(paths: string[], path: string | undefined): void {
  if (!path) return;

  const normalized = resolve(path);
  if (!paths.includes(normalized)) {
    paths.push(normalized);
  }
}

function inferSiblingWorkspaceDir(stateDir: string): string | undefined {
  const stateDirName = basename(stateDir);
  if (!stateDirName.startsWith(".")) return undefined;

  // JVSClaw may keep plugin state under /home/admin/.openclaw while the
  // runtime workspace is /home/admin/openclaw/workspace.
  const siblingStateDir = join(dirname(stateDir), stateDirName.slice(1));
  const siblingWorkspaceDir = join(siblingStateDir, "workspace");
  return existsSync(siblingWorkspaceDir) ? siblingWorkspaceDir : undefined;
}

function resolveBaseDirs(ctx: LightRuleStorageContext): string[] {
  const dirs: string[] = [];

  addUniquePath(dirs, ctx.workspaceDir);

  if (ctx.stateDir) {
    addUniquePath(dirs, inferSiblingWorkspaceDir(ctx.stateDir));

    const inferredWorkspaceDir = join(ctx.stateDir, "workspace");
    if (existsSync(inferredWorkspaceDir)) {
      addUniquePath(dirs, inferredWorkspaceDir);
    }

    addUniquePath(dirs, ctx.stateDir);
  }

  if (dirs.length === 0) {
    throw new Error("workspaceDir and stateDir both unavailable");
  }

  return dirs;
}

function resolveWriteBaseDir(ctx: LightRuleStorageContext): string {
  return resolveBaseDirs(ctx)[0];
}

function tasksDirs(ctx: LightRuleStorageContext): string[] {
  return resolveBaseDirs(ctx).map((baseDir) => join(baseDir, "tasks"));
}

function writeTasksDir(ctx: LightRuleStorageContext): string {
  return join(resolveWriteBaseDir(ctx), "tasks");
}

function normalizeLightRuleLookupName(name: string): string {
  return name.trim().replace(/\.json$/i, "");
}

function resolveLightRuleTask(
  ctx: LightRuleStorageContext,
  name: string,
): { taskDir: string; meta: LightRuleMeta } | null {
  const normalizedName = normalizeLightRuleLookupName(name);
  if (!normalizedName) return null;

  const dirs = tasksDirs(ctx);

  for (const dir of dirs) {
    const directTaskDir = join(dir, normalizedName);
    const directMeta = readMeta(directTaskDir);
    if (directMeta) {
      return {
        taskDir: directTaskDir,
        meta: directMeta,
      };
    }
  }

  for (const dir of dirs) {
    if (!existsSync(dir)) continue;

    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;

      const taskDir = join(dir, entry.name);
      const meta = readMeta(taskDir);
      if (meta?.name === normalizedName) {
        return {
          taskDir,
          meta,
        };
      }
    }
  }

  return null;
}

function readOptionalString(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed || undefined;
}

function readMeta(taskDir: string): LightRuleMeta | null {
  const metaPath = join(taskDir, "meta.json");
  if (!existsSync(metaPath)) return null;
  try {
    const raw = JSON.parse(readFileSync(metaPath, "utf-8"));
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
    if (raw.type !== "light-rule") return null;
    if (!Array.isArray(raw.segments)) return null;
    const name = readOptionalString(raw.name) ?? basename(taskDir);
    const title = readOptionalString(raw.title) ?? name;
    const description = readOptionalString(raw.description) ?? name;
    const createdAt =
      readOptionalString(raw.createdAt) ?? statSync(metaPath).birthtime.toISOString();
    const enabled = typeof raw.enabled === "boolean" ? raw.enabled : true;
    const repeatTimes = normalizeRepeatTimes({
      repeat: raw.repeat,
      repeat_times: raw.repeat_times,
    });
    assertAncsRepeatTimes(repeatTimes);
    return {
      ...raw,
      name,
      title,
      type: "light-rule",
      description,
      segments: raw.segments,
      repeat_times: repeatTimes,
      enabled,
      createdAt,
    } as LightRuleMeta;
  } catch {
    return null;
  }
}

function writeMeta(taskDir: string, meta: LightRuleMeta): void {
  writeFileSync(join(taskDir, "meta.json"), JSON.stringify(meta, null, 2), "utf-8");
}

export function listLightRules(ctx: LightRuleStorageContext): LightRuleMeta[] {
  const rules: LightRuleMeta[] = [];
  const seenNames = new Set<string>();

  for (const dir of tasksDirs(ctx)) {
    if (!existsSync(dir)) continue;

    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      const meta = readMeta(join(dir, entry.name));
      if (!meta || seenNames.has(meta.name)) continue;

      rules.push(meta);
      seenNames.add(meta.name);
    }
  }

  return rules;
}

export function getLightRule(
  ctx: LightRuleStorageContext,
  name: string,
): LightRuleMeta | null {
  return resolveLightRuleTask(ctx, name)?.meta ?? null;
}

export function createLightRule(
  ctx: LightRuleStorageContext,
  params: {
    name: string;
    title: string;
    description: string;
    segments: LightSegment[];
    repeat_times?: number;
    repeat?: boolean;
  },
): { meta: LightRuleMeta } {
  const existing = resolveLightRuleTask(ctx, params.name);
  if (existing) {
    throw new LightRuleError("ALREADY_EXISTS", `灯效规则 '${params.name}' 已存在`);
  }

  const dir = writeTasksDir(ctx);
  const taskDir = join(dir, params.name);

  if (existsSync(taskDir)) {
    throw new LightRuleError("ALREADY_EXISTS", `灯效规则 '${params.name}' 已存在`);
  }

  mkdirSync(taskDir, { recursive: true });

  const repeatTimes = normalizeRepeatTimes({
    repeat: params.repeat,
    repeat_times: params.repeat_times,
  });
  assertAncsRepeatTimes(repeatTimes);

  const meta: LightRuleMeta = {
    name: params.name,
    title: params.title,
    type: "light-rule",
    description: params.description,
    segments: params.segments,
    repeat_times: repeatTimes,
    enabled: true,
    createdAt: new Date().toISOString(),
  };

  writeMeta(taskDir, meta);

  return { meta };
}

export function updateLightRule(
  ctx: LightRuleStorageContext,
  params: {
    name: string;
    title?: string;
    description?: string;
    segments?: LightSegment[];
    repeat_times?: number;
    repeat?: boolean;
    enabled?: boolean;
  },
): { meta: LightRuleMeta } {
  const resolved = resolveLightRuleTask(ctx, params.name);
  const taskDir = resolved?.taskDir;
  const meta = resolved?.meta;

  if (!taskDir || !meta) {
    throw new LightRuleError("NOT_FOUND", `灯效规则 '${params.name}' 不存在`);
  }

  if (params.description !== undefined) {
    meta.description = params.description;
  }
  if (params.title !== undefined) {
    meta.title = params.title;
  }
  if (params.segments !== undefined) {
    meta.segments = params.segments;
  }
  if (params.repeat !== undefined || params.repeat_times !== undefined) {
    meta.repeat_times = normalizeRepeatTimes({
      repeat: params.repeat,
      repeat_times: params.repeat_times,
    });
    assertAncsRepeatTimes(meta.repeat_times);
  }
  if (params.enabled !== undefined) {
    meta.enabled = params.enabled;
  }

  meta.updatedAt = new Date().toISOString();
  writeMeta(taskDir, meta);

  return { meta };
}

export function deleteLightRule(
  ctx: LightRuleStorageContext,
  name: string,
): { name: string } {
  const resolved = resolveLightRuleTask(ctx, name);
  const taskDir = resolved?.taskDir;
  const meta = resolved?.meta;

  if (!taskDir || !meta) {
    throw new LightRuleError("NOT_FOUND", `灯效规则 '${name}' 不存在`);
  }

  rmSync(taskDir, { recursive: true, force: true });

  return { name: meta.name };
}

export class LightRuleError extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = "LightRuleError";
  }
}

/**
 * migrate from-openclaw —— 把 ~/.openclaw 插件数据接管到 ~/.yoooclaw/profiles/<profile>。
 */
import { cpSync, existsSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { CliContext } from "../context.js";
import { ensureDir, readJsonFile, writeJsonFile, SECRET_FILE_MODE } from "../fs-utils.js";
import { sharedCredentialsPath } from "../paths.js";
import { API_KEY_FIELD } from "../credentials/store.js";

interface MigrateOpts {
  dryRun?: boolean;
  source?: string;
}

interface SubdirPlan {
  name: string;
  source: string;
  target: string;
  exists: boolean;
  fileCount: number;
}

function countFiles(dir: string): number {
  if (!existsSync(dir)) return 0;
  try {
    return readdirSync(dir, { recursive: true } as never).length;
  } catch {
    return 0;
  }
}

export function migrateFromOpenclaw(
  ctx: CliContext,
  _args: unknown[],
  opts: MigrateOpts,
): unknown {
  const sourceRoot = opts.source ?? join(homedir(), ".openclaw");
  const pluginRoot = join(sourceRoot, "plugins", "phone-notifications");

  const plans: SubdirPlan[] = [
    { name: "notifications", source: join(pluginRoot, "notifications"), target: ctx.paths.notifications },
    { name: "recordings", source: join(pluginRoot, "recordings"), target: ctx.paths.recordings },
    { name: "light-rules", source: join(pluginRoot, "light-rules"), target: ctx.paths.lightRules },
    { name: "images", source: join(pluginRoot, "images"), target: ctx.paths.images },
  ].map((p) => ({ ...p, exists: existsSync(p.source), fileCount: countFiles(p.source) }));

  // api-key 迁移计划
  const srcCreds = readJsonFile<Record<string, unknown>>(join(sourceRoot, "credentials.json"));
  const srcApiKey = typeof srcCreds?.[API_KEY_FIELD] === "string" ? (srcCreds[API_KEY_FIELD] as string) : undefined;
  const sharedPath = sharedCredentialsPath();
  const existingShared = readJsonFile<Record<string, unknown>>(sharedPath);
  const apiKeyAction = !srcApiKey
    ? "none"
    : existingShared?.[API_KEY_FIELD]
      ? "skip-existing"
      : "copy";

  const backupDir = `${ctx.paths.dir}.bak-${Date.now()}`;
  const willBackup = existsSync(ctx.paths.dir) && readdirSync(ctx.paths.dir).length > 0;

  if (opts.dryRun) {
    return {
      ok: true,
      dryRun: true,
      source: sourceRoot,
      profile: ctx.profile,
      backup: willBackup ? backupDir : null,
      subdirs: plans,
      apiKey: { action: apiKeyAction },
      hint: "迁移前请先停止 openclaw 客户端，避免插件还在写 notifications 导致数据竞争",
    };
  }

  if (willBackup) {
    cpSync(ctx.paths.dir, backupDir, { recursive: true });
  }
  ensureDir(ctx.paths.dir);

  const migrated: string[] = [];
  for (const plan of plans) {
    if (!plan.exists) continue;
    cpSync(plan.source, plan.target, { recursive: true });
    migrated.push(plan.name);
  }

  let apiKeyResult = apiKeyAction;
  if (apiKeyAction === "copy" && srcApiKey) {
    const data = existingShared ?? {};
    data[API_KEY_FIELD] = srcApiKey;
    writeJsonFile(sharedPath, data, SECRET_FILE_MODE);
    apiKeyResult = "copied";
  }

  return {
    ok: true,
    source: sourceRoot,
    profile: ctx.profile,
    backup: willBackup ? backupDir : null,
    migrated,
    apiKey: { action: apiKeyResult },
  };
}

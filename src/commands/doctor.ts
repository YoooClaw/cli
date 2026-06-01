/**
 * doctor —— 环境自检。默认仅做**无网络、无 keychain 也确定性**的本地检查（PRD 可测试性要求）。
 * 网络类自检（relay / OSS 出站可达性）交给 gateway test / tunnel +test。
 */
import { accessSync, constants, existsSync, statSync } from "node:fs";
import type { CliContext } from "../context.js";
import { ensureDir } from "../fs-utils.js";
import { rootDir } from "../paths.js";
import { configExists, loadConfig } from "../config/store.js";
import { resolveApiKey, resolveGatewayToken } from "../credentials/store.js";
import { keychainAvailable } from "../credentials/keychain.js";
import { daemonState } from "../daemon/lock.js";

type CheckStatus = "ok" | "warn" | "fail" | "skip";
interface Check {
  name: string;
  status: CheckStatus;
  detail: string;
}

const MIN_NODE = [22, 12, 0];

function checkNode(): Check {
  const parts = process.versions.node.split(".").map(Number);
  let ok = true;
  for (let i = 0; i < 3; i += 1) {
    if ((parts[i] ?? 0) > MIN_NODE[i]) break;
    if ((parts[i] ?? 0) < MIN_NODE[i]) { ok = false; break; }
  }
  return {
    name: "node-version",
    status: ok ? "ok" : "fail",
    detail: `Node ${process.versions.node}（要求 >= ${MIN_NODE.join(".")}）`,
  };
}

function checkDir(name: string, dir: string, fix: boolean): Check {
  if (!existsSync(dir)) {
    if (fix) {
      ensureDir(dir);
      return { name, status: "ok", detail: `已创建 ${dir}` };
    }
    return { name, status: "warn", detail: `目录不存在：${dir}（--fix 可创建）` };
  }
  try {
    accessSync(dir, constants.R_OK | constants.W_OK);
  } catch {
    return { name, status: "fail", detail: `目录不可读写：${dir}` };
  }
  const mode = statSync(dir).mode & 0o777;
  const tooOpen = (mode & 0o077) !== 0;
  return {
    name,
    status: tooOpen ? "warn" : "ok",
    detail: `${dir}（mode ${mode.toString(8)}${tooOpen ? "，建议收紧到 700" : ""}）`,
  };
}

export function doctor(
  ctx: CliContext,
  _args: unknown[],
  opts: { json?: boolean; fix?: boolean },
): unknown {
  const fix = Boolean(opts.fix);
  const checks: Check[] = [checkNode(), checkDir("root-dir", rootDir(), fix)];

  // profile 配置
  if (configExists(ctx.paths)) {
    try {
      loadConfig(ctx.paths);
      checks.push({ name: "profile-config", status: "ok", detail: `${ctx.paths.config} 可解析` });
    } catch (err) {
      checks.push({ name: "profile-config", status: "fail", detail: (err as Error).message });
    }
  } else {
    checks.push({
      name: "profile-config",
      status: "warn",
      detail: `profile \`${ctx.profile}\` 未初始化（yoooclaw config init）`,
    });
  }

  // api-key
  const apiKey = resolveApiKey();
  checks.push({
    name: "api-key",
    status: apiKey.value ? "ok" : "warn",
    detail: apiKey.value ? `来源 ${apiKey.source}` : "未配置（yoooclaw auth set-api-key）",
  });

  // gateway token
  if (configExists(ctx.paths)) {
    const token = resolveGatewayToken(loadConfig(ctx.paths));
    checks.push({
      name: "gateway-token",
      status: token.value ? "ok" : "warn",
      detail: token.value ? `来源 ${token.source}` : "未设置（yoooclaw auth token-rotate）",
    });
  }

  // keychain（可选加固，不可用只是 skip）
  checks.push({
    name: "keychain",
    status: keychainAvailable() ? "ok" : "skip",
    detail: keychainAvailable() ? "可用" : "当前平台无可用 keychain，凭据将落文件",
  });

  // daemon（不在跑只是 info/warn）
  const state = daemonState(ctx.paths);
  checks.push({
    name: "daemon",
    status: state.running ? "ok" : state.stale ? "warn" : "skip",
    detail: state.running
      ? `运行中（pid ${state.lock?.pid}）`
      : state.stale
        ? "锁文件存在但进程已死（陈旧锁）"
        : "未运行",
  });

  const failed = checks.filter((c) => c.status === "fail").length;
  const warned = checks.filter((c) => c.status === "warn").length;
  return {
    ok: failed === 0,
    profile: ctx.profile,
    summary: { total: checks.length, failed, warned },
    checks,
    note: "网络类自检（relay / OSS 可达性）请用 yoooclaw gateway test / tunnel +test",
  };
}

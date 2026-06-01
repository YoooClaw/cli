/**
 * update self —— 查 npm registry 比对版本并提示。
 * 不做自动替换：升级方式取决于安装来源（CLI_DIST），命令文本相应区分。
 */
import { arch as nodeArch, platform as nodePlatform } from "node:os";
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { CLI_DIST, CLI_VERSION } from "../version.js";

const PACKAGE = "@yoooclaw/cli";
const REGISTRY = "https://registry.npmjs.org";
const INSTALL_SH = "https://raw.githubusercontent.com/Yoooclaw/openclaw-plugin/master/packages/cli/scripts/install.sh";

interface UpdateOpts {
  beta?: boolean;
  json?: boolean;
}

function normalizeOs(p: string): "darwin" | "linux" | null {
  if (p === "darwin") return "darwin";
  if (p === "linux") return "linux";
  return null;
}

function normalizeArch(a: string): "arm64" | "x64" | null {
  if (a === "arm64") return "arm64";
  if (a === "x64") return "x64";
  return null;
}

function nativeTargetName(): string | null {
  const os = normalizeOs(nodePlatform());
  const arch = normalizeArch(nodeArch());
  if (!os || !arch) return null;
  return `yoooclaw-${os}-${arch}`;
}

function upgradeCommand(channel: "latest" | "beta", latest: string): string {
  if (CLI_DIST === "native") {
    const target = nativeTargetName();
    if (!target) {
      return `curl -fsSL ${INSTALL_SH} | sh`;
    }
    return `curl -fsSL ${INSTALL_SH} | sh -s -- --version ${latest}`;
  }
  // npm 与 dev 形态都按 npm 升级（dev 形态走源码，提示文案给用户参考）
  if (channel === "beta") return `npm i -g ${PACKAGE}@beta`;
  return `npm update -g ${PACKAGE}`;
}

function compareSemver(a: string, b: string): number {
  const pa = a.split("-")[0].split(".").map(Number);
  const pb = b.split("-")[0].split(".").map(Number);
  for (let i = 0; i < 3; i += 1) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d > 0 ? 1 : -1;
  }
  return 0;
}

export async function updateSelf(
  _ctx: CliContext,
  _args: unknown[],
  opts: UpdateOpts,
): Promise<unknown> {
  const tag = opts.beta ? "beta" : "latest";
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 8000);
  let latest: string;
  try {
    const res = await fetch(`${REGISTRY}/${encodeURIComponent(PACKAGE)}`, {
      signal: controller.signal,
      headers: { Accept: "application/vnd.npm.install-v1+json" },
    });
    if (!res.ok) {
      throw new YoooclawError("YOOOCLAW_NETWORK_ERROR", `npm registry 返回 ${res.status}`);
    }
    const body = (await res.json()) as { "dist-tags"?: Record<string, string> };
    const resolved = body["dist-tags"]?.[tag];
    if (!resolved) {
      throw new YoooclawError("YOOOCLAW_NOT_FOUND", `npm dist-tag \`${tag}\` 不存在`);
    }
    latest = resolved;
  } catch (err) {
    if (err instanceof YoooclawError) throw err;
    throw new YoooclawError("YOOOCLAW_NETWORK_ERROR", `查询 npm registry 失败：${(err as Error).message}`);
  } finally {
    clearTimeout(timer);
  }

  const updateAvailable = compareSemver(latest, CLI_VERSION) > 0;
  return {
    ok: true,
    package: PACKAGE,
    channel: tag,
    dist: CLI_DIST,
    current: CLI_VERSION,
    latest,
    updateAvailable,
    command: updateAvailable ? upgradeCommand(tag, latest) : null,
    hint: updateAvailable
      ? "发现新版本；CLI 不做自动更新，请手动执行上面的命令"
      : "已是最新版本",
  };
}

/**
 * 用 `bun build --compile` 把 yoooclaw CLI 打成原生单文件可执行（内嵌 Bun runtime）。
 *
 * 与 build.ts 共享 src/ 与 host env 注入逻辑，区别在于：
 *   - 不 external 任何依赖（compile 模式必须内联）
 *   - 注入 __CLI_DIST__ = "native"，update self 据此决定升级路径
 *   - 产物落在 dist-native/yoooclaw-<os>-<arch>{,.exe}
 *
 * 用法：
 *   bun scripts/build-native.ts                 # 4 个目标全构（CI 用）
 *   bun scripts/build-native.ts --target current# 只构当前平台（本地验证用）
 *   bun scripts/build-native.ts --target darwin-arm64,linux-x64
 */
import { existsSync } from "node:fs";
import { readFile, rm, stat } from "node:fs/promises";
import { arch as nodeArch, platform as nodePlatform } from "node:os";
import { join } from "node:path";
import pkg from "../package.json" with { type: "json" };

interface Target {
  os: "darwin" | "linux" | "windows";
  arch: "x64" | "arm64";
  bunTarget: string;
}

const ALL_TARGETS: Target[] = [
  { os: "darwin", arch: "arm64", bunTarget: "bun-darwin-arm64" },
  { os: "darwin", arch: "x64", bunTarget: "bun-darwin-x64" },
  { os: "linux", arch: "x64", bunTarget: "bun-linux-x64" },
  { os: "linux", arch: "arm64", bunTarget: "bun-linux-arm64" },
];

const manifest = pkg as { version?: string };
const version = manifest.version ?? "unknown";

// 与 build.ts 同源：构建期注入的 Relay host。
// （灯效 API 的 appKey / templateId 已写死在 sender.ts，不再走 env 注入。）
const INJECTED_ENV_KEYS = [
  "OPENCLAW_HOST_PRODUCTION",
  "OPENCLAW_HOST_TEST",
  "OPENCLAW_HOST_DEVELOPMENT",
] as const;

async function loadPluginBuildEnv(): Promise<Record<string, string>> {
  // 独立 CLI 仓自带 .env；CI 中不存在时回退 process.env。
  const envPath = join(import.meta.dir, "..", ".env");
  if (!existsSync(envPath)) return {};
  const raw = await readFile(envPath, "utf-8");
  const out: Record<string, string> = {};
  const wanted = new Set<string>(INJECTED_ENV_KEYS);
  for (const line of raw.split("\n")) {
    const eq = line.indexOf("=");
    if (eq < 1 || line.trimStart().startsWith("#")) continue;
    const key = line.slice(0, eq).trim();
    if (wanted.has(key)) out[key] = line.slice(eq + 1).trim();
  }
  return out;
}

function currentTarget(): Target | undefined {
  const p = nodePlatform();
  const a = nodeArch();
  const os: Target["os"] | undefined =
    p === "darwin" ? "darwin" : p === "linux" ? "linux" : undefined;
  const arch: Target["arch"] | undefined =
    a === "arm64" ? "arm64" : a === "x64" ? "x64" : undefined;
  if (!os || !arch) return undefined;
  return ALL_TARGETS.find((t) => t.os === os && t.arch === arch);
}

function parseTargetsArg(): Target[] {
  const idx = Bun.argv.findIndex((a) => a === "--target" || a === "-t");
  if (idx === -1) return ALL_TARGETS;
  const value = Bun.argv[idx + 1];
  if (!value) {
    throw new Error("--target 需要值（current / all / <os>-<arch>[,...]）");
  }
  if (value === "all") return ALL_TARGETS;
  if (value === "current") {
    const t = currentTarget();
    if (!t) throw new Error(`当前平台 ${nodePlatform()}/${nodeArch()} 不在目标矩阵中`);
    return [t];
  }
  const wanted = value.split(",").map((s) => s.trim()).filter(Boolean);
  const resolved: Target[] = [];
  for (const w of wanted) {
    const t = ALL_TARGETS.find((x) => `${x.os}-${x.arch}` === w);
    if (!t) throw new Error(`未知 target: ${w}（候选：${ALL_TARGETS.map((x) => `${x.os}-${x.arch}`).join(", ")}）`);
    resolved.push(t);
  }
  return resolved;
}

async function buildOne(target: Target, buildEnv: Record<string, string>): Promise<string> {
  const outDir = "dist-native";
  const outName = `yoooclaw-${target.os}-${target.arch}${target.os === "windows" ? ".exe" : ""}`;
  const outPath = join(outDir, outName);

  // 通过 --define 注入构建期常量；bun CLI 支持 string-form value（同 JS API）
  const defines: Record<string, string> = {
    __CLI_VERSION__: JSON.stringify(version),
    __CLI_DIST__: JSON.stringify("native"),
  };
  for (const key of INJECTED_ENV_KEYS) {
    defines[`process.env.${key}`] = JSON.stringify(buildEnv[key] ?? process.env[key] ?? "");
  }

  const args = [
    "build",
    "src/bin.ts",
    "--compile",
    `--target=${target.bunTarget}`,
    `--outfile=${outPath}`,
  ];
  for (const [k, v] of Object.entries(defines)) {
    args.push("--define", `${k}=${v}`);
  }

  const t0 = Date.now();
  const proc = Bun.spawn(["bun", ...args], { stdout: "inherit", stderr: "inherit" });
  const code = await proc.exited;
  if (code !== 0) {
    throw new Error(`bun build --compile 失败（${target.bunTarget}），exit=${code}`);
  }
  const elapsed = Date.now() - t0;
  const stats = await stat(outPath);
  const sizeMb = (stats.size / 1024 / 1024).toFixed(1);
  console.log(`[native] ${target.os}-${target.arch}  ${sizeMb} MB  ${elapsed} ms  -> ${outPath}`);
  return outPath;
}

async function main(): Promise<void> {
  const targets = parseTargetsArg();
  console.log(`[native] CLI v${version} -> ${targets.length} target(s)`);

  await rm("dist-native", { recursive: true, force: true });
  const buildEnv = await loadPluginBuildEnv();

  const built: string[] = [];
  for (const target of targets) {
    built.push(await buildOne(target, buildEnv));
  }

  console.log(`[native] 完成 ${built.length} 个产物：`);
  for (const p of built) console.log(`  - ${p}`);
}

await main();

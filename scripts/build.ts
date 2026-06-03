/**
 * 使用 Bun 打包 yoooclaw CLI（对齐 phone-notifications 插件的构建方式）。
 *
 * 产物：
 *   - dist/bin.cjs    可执行入口（带 #!/usr/bin/env node shebang），package.json#bin 指向它
 *   - dist/index.cjs  程序化入口（供其他包 import）
 * 类型声明由 `tsc --emitDeclarationOnly` 单独生成。
 * 构建期注入 CLI 版本 __CLI_VERSION__，以及 Relay 生产 host（与插件 env.ts 同源）。
 */
import { copyFile, mkdir, readFile, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { existsSync } from "node:fs";
import pkg from "../package.json" with { type: "json" };

const manifest = pkg as {
  version?: string;
  dependencies?: Record<string, string>;
};
const version = manifest.version ?? "unknown";

// dependencies 默认外置（npm 安装时由 node_modules 提供）；ws 在 devDependencies 中，
// 因此会被打进产物（Relay 客户端依赖 ws），与插件 noExternal 一致。
const externalNames = Object.keys(manifest.dependencies ?? {});
const external = externalNames.flatMap((name) => [name, `${name}/*`]);

// 复用 phone-notifications 那套 Relay：从插件 .env 读取 host，注入与插件相同的
// process.env.*。这样 schema.ts 的 DEFAULT_RELAY_URL 在构建期就被替换为生产 relay
// 地址。（灯效 API 的 appKey / templateId 已写死在 sender.ts，不再走 env 注入。）
const INJECTED_ENV_KEYS = [
  "OPENCLAW_HOST_PRODUCTION",
  "OPENCLAW_HOST_TEST",
  "OPENCLAW_HOST_DEVELOPMENT",
] as const;

async function loadPluginEnv(): Promise<Record<string, string>> {
  // 独立 CLI 仓自带 .env（本地构建注入 Relay host）；CI 中不存在时回退 process.env。
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

const pluginEnv = await loadPluginEnv();
const hostDefines: Record<string, string> = {};
for (const key of INJECTED_ENV_KEYS) {
  hostDefines[`process.env.${key}`] = JSON.stringify(pluginEnv[key] ?? process.env[key] ?? "");
}

await rm("dist", { recursive: true, force: true });

const result = await Bun.build({
  entrypoints: ["src/bin.ts", "src/index.ts"],
  outdir: "dist",
  target: "node",
  format: "cjs",
  sourcemap: "linked",
  external,
  naming: "[dir]/[name].cjs",
  banner: "#!/usr/bin/env node",
  define: {
    __CLI_VERSION__: JSON.stringify(version),
    __CLI_DIST__: JSON.stringify("npm"),
    ...hostDefines,
  },
});

if (!result.success) {
  for (const log of result.logs) {
    console.error(log);
  }
  process.exit(1);
}

console.log(`[build] 生成 ${result.outputs.length} 个文件 -> dist/`);

const tsc = Bun.spawn(
  ["tsc", "--emitDeclarationOnly", "--declaration", "--declarationMap", "false"],
  {
    stdout: "inherit",
    stderr: "inherit",
  },
);
const tscExitCode = await tsc.exited;
if (tscExitCode !== 0) {
  process.exit(tscExitCode);
}

const publicDeclarationFiles = [
  "index.d.ts",
  "program.d.ts",
  "command-tree.d.ts",
  "version.d.ts",
  "errors.d.ts",
  "output/format.d.ts",
  "context.d.ts",
  "paths.d.ts",
];

for (const file of publicDeclarationFiles) {
  const source = join("dist", "src", file);
  const target = join("dist", file);
  await mkdir(dirname(target), { recursive: true });
  await copyFile(source, target);
}

// 仅保留上面的公开类型入口，删除其余深层声明（含 vendor/）。
await rm(join("dist", "src"), { recursive: true, force: true });

console.log(`[build] 生成 ${publicDeclarationFiles.length} 个类型声明入口 -> dist/`);

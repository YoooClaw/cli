#!/usr/bin/env node
"use strict";

// @yoooclaw/cli launcher —— 解析当前平台对应的 optionalDependency 子包里的 Go
// 二进制并执行它，透传 argv 与退出码。子包按 package.json 的 os/cpu 字段由 npm
// 自动只安装匹配当前平台的那一个。
//
// 设计与 esbuild / turbo 一致：主包不含二进制，只含这个极薄 launcher。

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

function packageVersion() {
  try {
    return require("../package.json").version;
  } catch {
    return "";
  }
}

function isFile(candidate) {
  if (!candidate) return false;
  try {
    return fs.statSync(candidate).isFile();
  } catch {
    return false;
  }
}

// OpenHarmony does not allow an application sandbox to execute an ELF that was
// downloaded into app data by npm. Native commands must be installed as an HNP
// inside the host HAP. Resolve that preinstalled binary instead of pretending the
// Linux optionalDependency can be executed from node_modules.
function resolveOpenHarmonyBinary(
  env = process.env,
  version = packageVersion(),
  fileExists = isFile,
) {
  const candidates = [env.YOOOCLAW_NATIVE_BIN];

  if (version) {
    const relative = path.join(
      "yoooclaw.org",
      `yoooclaw_${version}`,
      "bin",
      "yoooclaw",
    );
    candidates.push(
      path.join(env.HNP_PRIVATE_HOME || "/data/app", relative),
      path.join(env.HNP_PUBLIC_HOME || "/data/service/hnp", relative),
    );
  }

  // HNP links live under the private/public bin directories. Keep a unique name
  // so it cannot resolve back to this npm launcher through a global symlink.
  candidates.push(
    path.join(env.HNP_PRIVATE_HOME || "/data/app", "bin", "yoooclaw-native"),
    "/data/app/bin/yoooclaw-native",
    path.join(env.HNP_PUBLIC_HOME || "/data/service/hnp", "bin", "yoooclaw-native"),
    "/data/service/hnp/bin/yoooclaw-native",
  );

  return candidates.find(fileExists) || null;
}

function resolveBinary(platform = process.platform, arch = process.arch) {
  if (platform === "openharmony") {
    return resolveOpenHarmonyBinary();
  }
  const pkg = `@yoooclaw/cli-${platform}-${arch}`;
  const binName = platform === "win32" ? "yc.exe" : "yc";
  try {
    return require.resolve(`${pkg}/bin/${binName}`);
  } catch {
    return null;
  }
}

function missingBinaryMessage(platform = process.platform, arch = process.arch) {
  const target = `${platform}-${arch}`;
  if (platform === "openharmony") {
    return (
      `@yoooclaw/cli: 找不到宿主预装的 OpenHarmony HNP 二进制（${target}）。\n` +
      "鸿蒙应用沙箱禁止执行 npm 下载到应用数据目录的原生 ELF；chmod 或重新安装无效。\n" +
      "请让 WorkBuddy/OpenClaw 宿主把 yoooclaw.hnp 嵌入并签入 HAP，\n" +
      "或用 YOOOCLAW_NATIVE_BIN 指向已经由 HNP 安装的 yoooclaw。\n"
    );
  }
  return (
    `@yoooclaw/cli: 找不到当前平台的二进制（${target}）。\n` +
    "可能原因：\n" +
    "  - 该平台暂不支持（当前支持 darwin/linux 的 x64+arm64、win32 的 x64）\n" +
    "  - 安装时跳过了 optionalDependencies（--no-optional / --omit=optional）\n" +
    "请尝试重新安装： npm i -g @yoooclaw/cli\n"
  );
}

function main() {
  const bin = resolveBinary();
  if (!bin) {
    process.stderr.write(missingBinaryMessage());
    return 1;
  }

  if (process.platform !== "win32") {
    try {
      fs.chmodSync(bin, 0o755);
    } catch {
      // Best effort only: spawnSync will surface the real error if execution still fails.
    }
  }

  const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
  if (result.error) {
    process.stderr.write(`@yoooclaw/cli: 启动二进制失败：${result.error.message}\n`);
    if (process.platform === "openharmony" && result.error.code === "EACCES") {
      process.stderr.write(
        "该文件不是由 HNP/HAP 安装的可执行文件；请勿从 node_modules 手工复制。\n",
      );
    }
    return 1;
  }
  // 子进程被信号杀死时 status 为 null；统一以非零码退出。
  return typeof result.status === "number" ? result.status : 1;
}

if (require.main === module) {
  process.exit(main());
}

module.exports = {
  missingBinaryMessage,
  resolveBinary,
  resolveOpenHarmonyBinary,
};

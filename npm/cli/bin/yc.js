#!/usr/bin/env node
"use strict";

// @yoooclaw/cli launcher —— 解析当前平台对应的 optionalDependency 子包里的 Go
// 二进制并执行它，透传 argv 与退出码。子包按 package.json 的 os/cpu 字段由 npm
// 自动只安装匹配当前平台的那一个。
//
// 设计与 esbuild / turbo 一致：主包不含二进制，只含这个极薄 launcher。

const { spawnSync } = require("node:child_process");

function resolveBinary() {
  const platform = process.platform; // darwin | linux | win32
  const arch = process.arch; // arm64 | x64
  const pkg = `@yoooclaw/cli-${platform}-${arch}`;
  const binName = platform === "win32" ? "yc.exe" : "yc";
  try {
    return require.resolve(`${pkg}/bin/${binName}`);
  } catch {
    return null;
  }
}

const bin = resolveBinary();
if (!bin) {
  const target = `${process.platform}-${process.arch}`;
  process.stderr.write(
    `@yoooclaw/cli: 找不到当前平台的二进制（${target}）。\n` +
      "可能原因：\n" +
      "  - 该平台暂不支持（当前支持 darwin/linux 的 x64+arm64、win32 的 x64）\n" +
      "  - 安装时跳过了 optionalDependencies（--no-optional / --omit=optional）\n" +
      "请尝试重新安装： npm i -g @yoooclaw/cli\n",
  );
  process.exit(1);
}

const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  process.stderr.write(`@yoooclaw/cli: 启动二进制失败：${result.error.message}\n`);
  process.exit(1);
}
// 子进程被信号杀死时 status 为 null；统一以非零码退出。
process.exit(typeof result.status === "number" ? result.status : 1);

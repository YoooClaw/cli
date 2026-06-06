// 生成 npm 包的 package.json（主 launcher 包 / 各平台子包）。
// 由 scripts/build-go.sh 调用；独立成文件以免在 shell 里手拼 JSON。
//
// 用法：
//   node scripts/gen-pkg.mjs platform <dir> <version> <npmOs> <npmCpu> <binName>
//   node scripts/gen-pkg.mjs main     <dir> <version> "<platformPkgName> ..."
import { writeFileSync } from "node:fs";
import { join } from "node:path";

const [mode, dir, version, ...rest] = process.argv.slice(2);

const COMMON = {
  version,
  license: "MIT",
  author: "developer@yoooclaw.com",
  repository: { type: "git", url: "git+https://github.com/YoooClaw/cli.git" },
  publishConfig: { access: "public" },
};

function write(dir, pkg) {
  writeFileSync(join(dir, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
}

if (mode === "platform") {
  const [npmOs, npmCpu, binName] = rest;
  write(dir, {
    name: `@yoooclaw/cli-${npmOs}-${npmCpu}`,
    description: `yoooclaw CLI 原生二进制（${npmOs}-${npmCpu}）`,
    ...COMMON,
    // os/cpu 让 npm 只在匹配平台安装本子包；files 限定只发布二进制。
    os: [npmOs],
    cpu: [npmCpu],
    files: [`bin/${binName}`],
  });
} else if (mode === "main") {
  const deps = (rest[0] ?? "").trim().split(/\s+/).filter(Boolean);
  const optionalDependencies = {};
  for (const name of deps.sort()) optionalDependencies[name] = version;
  write(dir, {
    name: "@yoooclaw/cli",
    description: "yoooclaw 独立 CLI（Go 实现，按平台分发原生二进制）",
    ...COMMON,
    bin: { yoooclaw: "bin/yc.js", yc: "bin/yc.js" },
    files: ["bin/yc.js", "README.md"],
    optionalDependencies,
    engines: { node: ">=18" },
    keywords: ["yoooclaw", "cli", "daemon", "notifications", "agent"],
  });
} else {
  console.error(`gen-pkg: 未知 mode "${mode}"（应为 platform | main）`);
  process.exit(1);
}

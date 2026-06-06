# @yoooclaw/cli

yoooclaw 独立 CLI（Go 实现）。npm 包本身只含一个极薄的 Node launcher；真正的可执行
是按平台分发的原生 Go 二进制，通过 `optionalDependencies` 子包安装，npm 会根据
`os`/`cpu` 字段自动只装匹配当前平台的那一个。

## 安装

```bash
npm i -g @yoooclaw/cli
# 提供 yoooclaw 与 yc 两个命令
yc --version
```

也可不经 npm，直接下载原生二进制（见 GitHub Release / install.sh）。

## 支持平台

| OS | Arch |
|----|------|
| macOS (darwin) | arm64, x64 |
| Linux | x64, arm64 |
| Windows (win32) | x64 |

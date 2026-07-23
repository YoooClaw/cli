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

`darwin/linux` 的 `x64+arm64` 与 `win32-x64` 可直接通过 npm 安装。

OpenHarmony/鸿蒙 PC 的应用沙箱不允许执行 npm 运行时下载到应用数据目录的原生
ELF，因此不能只靠 `npm i -g` 安装。宿主应用需要把仓库生成的 `yoooclaw.hnp`
嵌入并签入 HAP；launcher 会自动查找该 HNP 二进制，也可以通过
`YOOOCLAW_NATIVE_BIN` 显式指定安装后的路径。

| OS | Arch |
|----|------|
| macOS (darwin) | arm64, x64 |
| Linux | x64, arm64 |
| Windows (win32) | x64 |

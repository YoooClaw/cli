# WorkBuddy 接入 Yoooclaw CLI（鸿蒙 PC）

本文面向 WorkBuddy/HarmonyOS 宿主应用开发者。最终用户不能在已经安装的 WorkBuddy
应用沙箱里直接安装 `.hnp`；必须把 HNP 嵌入 WorkBuddy 的 HAP，重新构建并签名发布。

## 交付物

- `yoooclaw.hnp`：OpenHarmony ARM64 原生包。
- `launcher/yc.js`：能够发现私有/公共 HNP 的 Node launcher。
- `SHA256SUMS`：HNP 包校验值。

不要解压或通过 `npm i -g` 安装这个 HNP。npm 只能安装 JavaScript launcher，无法给
运行时下载的 ELF 添加鸿蒙所需的执行标签。

## 一、把 HNP 放入 WorkBuddy HAP 工程

在 WorkBuddy 的 HAP 工程根目录创建以下结构：

```text
<workbuddy-hap-root>/
├── entry/
│   └── src/main/module.json5
└── hnp/
    └── arm64-v8a/
        └── yoooclaw.hnp
```

也就是把交付的 `yoooclaw.hnp` 复制到：

```text
<workbuddy-hap-root>/hnp/arm64-v8a/yoooclaw.hnp
```

## 二、声明私有 HNP

编辑 `entry/src/main/module.json5`，在现有的 `module` 对象中加入
`hnpPackages`。不要新建第二个 `module` 对象。

```json5
{
  "module": {
    // WorkBuddy 原有配置保持不变
    "hnpPackages": [
      {
        "package": "yoooclaw.hnp",
        "type": "private",
        "independentSign": false
      }
    ]
  }
}
```

推荐使用 `private`：CLI 只供 WorkBuddy 使用，可避免与其他 HAP 安装的同名公共 HNP
冲突。`independentSign: false` 表示随 WorkBuddy HAP 一起签名。

## 三、接入 Node launcher

在构建 WorkBuddy HAP 前，用交付包内的 `launcher/yc.js` 替换/覆盖 WorkBuddy 源码中
随应用打包的 `@yoooclaw/cli/bin/yc.js`。这是构建期操作，不要在已经安装的 WorkBuddy
应用数据目录里手改文件。

新版 launcher 会依次查找：

1. 环境变量 `YOOOCLAW_NATIVE_BIN`；
2. 私有 HNP 的版本路径；
3. 公共 HNP 的 `yoooclaw-native` 链接。

如果 WorkBuddy 不方便更新 npm launcher，可在启动 CLI 前显式设置：

```bash
export YOOOCLAW_NATIVE_BIN="${HNP_PRIVATE_HOME:-/data/app}/yoooclaw.org/yoooclaw_0.6.3/bin/yoooclaw"
```

也可以让 WorkBuddy 的 Node 进程直接设置同名环境变量，再调用 launcher。不要把 HNP
中的二进制复制到 `node_modules`；复制后的文件会重新变成不可执行的应用数据文件。

## 四、构建并签名 WorkBuddy HAP

使用 WorkBuddy 原有的 DevEco/OpenHarmony 构建流程生成 HAP，并使用 WorkBuddy 的
正式签名证书签名。HNP 必须出现在最终 HAP 中；仅把 `.hnp` 下载到设备用户目录不会
触发安装。

安装新版 WorkBuddy HAP 后，系统会随 HAP 安装私有 HNP。卸载 WorkBuddy 时，该私有
HNP 也会一起卸载。

## 五、设备验证

在新版 WorkBuddy 的终端/Node 执行环境中运行：

```bash
export YOOOCLAW_NATIVE_BIN="${HNP_PRIVATE_HOME:-/data/app}/yoooclaw.org/yoooclaw_0.6.3/bin/yoooclaw"

"$YOOOCLAW_NATIVE_BIN" --version
"$YOOOCLAW_NATIVE_BIN" doctor
```

第一条应输出 `0.6.3`，并且不再出现 `spawnSync ... EACCES`。然后验证 launcher：

```bash
yoooclaw --version
yc --version
```

最后验证需要子进程和本地监听的 daemon：

```bash
yoooclaw daemon start
yoooclaw daemon status
yoooclaw daemon stop
```

如果直接执行 HNP 路径成功、但 `yoooclaw --version` 失败，说明 Native 包已经正确安装，
需要更新 WorkBuddy 内的 `yc.js` launcher 或为它注入 `YOOOCLAW_NATIVE_BIN`。

## 六、不要采用的方案

- 不要把 `process.platform === "openharmony"` 简单映射为 `linux`。
- 不要手工下载 `@yoooclaw/cli-linux-arm64` 到全局 `node_modules`。
- 不要依赖 `chmod`、`chcon` 或 `setfattr` 修改应用数据目录里的 ELF。
- 不要把 `.hnp` 当普通压缩包解压到用户目录。

这些方法都不能让运行时下载的二进制获得 HAP/HNP 安装阶段赋予的执行权限。

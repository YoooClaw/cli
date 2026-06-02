# @yoooclaw/cli

yoooclaw 独立 CLI 工具 —— 自带后台守护进程（daemon），不依赖 openclaw 客户端在线。
设计对齐飞书 [lark-cli](https://github.com/larksuite/cli)：Service-oriented 命令树、三层命令体系（Shortcuts / Service Commands / Raw API）、统一 `--format`、Agent-Native。

完整文档见 [packages/docs/src/cli](../docs/src/cli)。

> **状态：可用。** 配置、认证、daemon、通知、录音、图片、灯效规则、监控、Relay、日志、gateway 自检和 raw API 等 service 命令已落地；本地查询类可纯读磁盘，控制类通过本地 daemon 协作。

## 安装与命令

两种分发渠道，二者**功能一致**，按需选择。

**平台支持**：macOS / Linux 两种渠道都支持；**Windows 走 npm 渠道**（需 Node ≥ 22.12.0），原生二进制暂未提供 Windows 目标。Windows 上凭据以明文落 `~/.yoooclaw/credentials.json`（无系统 keychain 加固，`yoooclaw doctor` 会提示），daemon 停止经 HTTP 优雅退出。

### A. npm（需要 Node ≥ 22.12.0，Windows 用此渠道）

```bash
# 免安装（每次拉最新版）
npx @yoooclaw/cli --help
npx @yoooclaw/cli notification +today

# 全局安装（提供 yoooclaw / yc 两个命令）
npm i -g @yoooclaw/cli
yoooclaw --help
yc --help
```

### B. 原生二进制（无需 Node）

单文件可执行（内嵌 Bun runtime），首次安装 ~60–90 MB，冷启动与 Node 相当。

```bash
# 自动检测平台、下载、校验 sha256、写到 ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/Yoooclaw/openclaw-plugin/master/packages/cli/scripts/install.sh | sh

# 指定版本 / 安装目录 / 覆盖
curl -fsSL https://raw.githubusercontent.com/Yoooclaw/openclaw-plugin/master/packages/cli/scripts/install.sh \
  | sh -s -- --version 0.0.5 --dir ~/bin --force
```

支持平台：`darwin-arm64` / `darwin-x64` / `linux-x64` / `linux-arm64`。**Windows 暂无原生二进制，请用上面的 npm 渠道。**

二进制也可以从 [GitHub Releases](https://github.com/Yoooclaw/openclaw-plugin/releases?q=cli-v) 手动下载（同 release 内 `checksums.txt` 校验）。

> `yoooclaw update self` 会按当前安装来源给出对应升级命令（npm 走 `npm update -g`，二进制走 install.sh）。

## 命令体系

- **Shortcuts**（`+` 前缀）：`yoooclaw notification +today`、`yoooclaw light +blink` …
- **Service commands**：`yoooclaw <service> <subcommand>`，service 见 `yoooclaw --help`。
- **Raw API**：`yoooclaw api <METHOD> <PATH> [--data ...]` 直达 daemon HTTP 端点。

### 全局 flags

| flag               | 说明                                              |
| ------------------ | ------------------------------------------------- |
| `--profile <name>` | 切换 profile（默认 `default`）                    |
| `--format <fmt>`   | `json\|pretty\|table\|ndjson`（TTY 默认 pretty，管道默认 json） |
| `--quiet`          | 抑制进度日志，只输出最终结果                      |
| `--no-color`       | 关闭终端颜色                                      |

### 输出契约

成功与失败共用同一通道（stdout）与可预测结构；失败额外以非零退出码表达：

```json
{ "ok": false, "error": { "code": "YOOOCLAW_DAEMON_NOT_RUNNING", "message": "...", "hint": "..." } }
```

错误码统一前缀 `YOOOCLAW_*`（见 [src/errors.ts](src/errors.ts)）。

## 录音与 Relay

独立 daemon 会复用 `@yoooclaw/phone-notifications` 的录音存储、状态机和 ASR 调度，并通过 `RelayClient + RelayDispatcher` 接收手机端 `recordings.sync` / `POST /recordings`。

```bash
# api 模式：未写 --api-key 时回退 account 级 ock- key
yoooclaw recording setup-asr --mode api --language auto --non-interactive

# 查询和跟随录音状态事件
yoooclaw recording list
yoooclaw recording events --since 1h --limit 50
yoooclaw recording events --id <recording-id> --watch
```

录音配置与事件分别落在当前 profile 的：

```text
~/.yoooclaw/profiles/<profile>/recordings/asr-config.json
~/.yoooclaw/profiles/<profile>/recordings/state/events.jsonl
```

## Agent Skill

随包发布 [skills/](skills/) 下的 SKILL.md（流式查通知、从 stdin 建灯效规则、隧道排查），教 Agent 直接调 `yoooclaw` 命令。在 openclaw 插件里这些 Skill 由 `openclaw.plugin.json` 自动注册；独立 CLI 形态下需手动安装到 Agent 的 skills 发现目录：

```bash
yoooclaw skills list                 # 列出随包发布的内置 Skill
yoooclaw skills targets              # 查看支持的 Agent 目标和探测结果
yoooclaw skills install              # 自动探测唯一 Agent 后软链安装
yoooclaw skills install --agent codex
yoooclaw skills install --agent claude
yoooclaw skills install --copy       # 复制而非软链（Windows 无管理员权限时用）
yoooclaw skills install --target <dir> --force
```

默认软链而非复制：`yoooclaw update self` 升级 CLI 后，Skill 内容自动跟随新版本。裸 `skills install` 只会在检测到唯一 Agent 时自动安装；否则显式传 `--agent claude` / `--agent codex` 或 `--target <dir>`。安装后重启 Agent 会话即可被发现。

## 数据目录

`~/.yoooclaw/`（可用 `YOOOCLAW_HOME` 覆盖，便于测试 / 多实例）。布局见 [src/paths.ts](src/paths.ts) 与 PRD「数据模型」。

## 开发

```bash
bun run build       # bun 打包 dist/bin.cjs + dist/index.cjs，tsc 出类型
bun run typecheck
bun run test
node dist/bin.cjs --help
```

## 源码结构

| 文件                                     | 职责                                       |
| ---------------------------------------- | ------------------------------------------ |
| [src/bin.ts](src/bin.ts)                 | 可执行入口（package.json#bin）             |
| [src/index.ts](src/index.ts)             | 程序化入口 `run(argv)` + 核心模块导出      |
| [src/command-tree.ts](src/command-tree.ts) | 命令树声明（单一事实来源）                 |
| [src/program.ts](src/program.ts)         | 据命令树构建 commander 程序 + action 包装  |
| [src/context.ts](src/context.ts)         | 全局 flags → CliContext，profile 解析      |
| [src/output/format.ts](src/output/format.ts) | `--format` 统一序列化 + 错误 schema    |
| [src/errors.ts](src/errors.ts)           | `YOOOCLAW_*` 错误码 + `YoooclawError`      |
| [src/paths.ts](src/paths.ts)             | `~/.yoooclaw/` 目录布局解析                |
| [src/daemon/recording-bridge.ts](src/daemon/recording-bridge.ts) | daemon 形态的 `recordings.sync` 覆盖、ASR fallback 与 in-flight 去重 |
| [src/daemon/recording-events.ts](src/daemon/recording-events.ts) | 录音状态事件 JSONL 追加日志 |
| [src/daemon/relay-dispatcher.ts](src/daemon/relay-dispatcher.ts) | Relay 入站帧到 `StandaloneRuntime` 的进程内分发 |
| [src/commands/skills.ts](src/commands/skills.ts) | 内置 Skill 列举 / 安装到 Agent skills 目录 |

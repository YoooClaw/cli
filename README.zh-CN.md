# @yoooclaw/cli

[English](README.md) | 简体中文

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![npm version](https://img.shields.io/npm/v/@yoooclaw/cli.svg)](https://www.npmjs.com/package/@yoooclaw/cli)
[![Go native](https://img.shields.io/badge/native-Go-00ADD8.svg)](https://go.dev/)

yoooclaw 独立 CLI 工具 —— **自带后台守护进程（daemon），不依赖 openclaw 客户端在线**，为人类与 AI Agent 而生。本地接收手机通知、Relay 隧道、录音 ASR、灯效规则评估，统一 `--format` 输出，随包发布 Agent Skill 开箱即用。

Service-oriented 命令树、三层命令体系、Agent-Native。

[安装](#安装与快速开始) · [Agent 技能](#agent-技能) · [鉴权](#鉴权) · [命令体系](#三层命令体系) · [进阶](#进阶用法) · [安全](#安全与风险提示) · [贡献](#开发与贡献)

## 为什么用 yoooclaw？

- **自带守护进程** —— 本地 daemon 收通知、跑规则、连 Relay，不依赖 openclaw 客户端在线
- **Agent-Native** —— 随包 [Skill](skills/) 开箱即用，Agent 零额外配置直接调 `yoooclaw` 命令
- **三层命令体系** —— Shortcuts（人/AI 友好）→ Service Commands（结构化）→ Raw API（全覆盖），按粒度选择
- **纯读磁盘的查询** —— 通知 / 语音输入 / 录音 / 图片 / 已同步网页查询直接读 `~/.yoooclaw`，不需要 daemon 在跑
- **统一输出契约** —— `--format json|pretty|table|ndjson`，成功失败同通道、结构可预测；本地 CLI 错误返回非零退出码，Raw/daemon HTTP 响应请同时检查 `ok` / HTTP status
- **凭据安全** —— OS keychain 优先存储，多 api-key 管理，gateway token 鉴权本地 ingest
- **Go 原生二进制** —— npm 薄 launcher + 平台子包，或直接安装原生 binary；macOS / Linux / Windows 全平台

## 能力一览

| 领域                  | 能力                                                         | daemon |
| --------------------- | ------------------------------------------------------------ | ------ |
| 📱 通知 Notification  | 按时间/应用/发送人/关键词查询，今日/最近摘要，多维聚合统计，大批量分片总结 | 🟢     |
| 🔄 同步 Sync          | 扫描/迭代未处理通知、按日期取详情、提交批次，供记忆系统消费   | 🟢     |
| 🗣️ 语音输入 Voice     | 列举/搜索每日 JSONL 口述历史、发现近期桌面 App ID/名称、检查可选音频 | 🟢     |
| 🎙️ 录音 Recording     | 统一查询 YoooClaw Capture 与智能硬件录音，并提供硬件 ASR 配置和事件流 | 🟢     |
| 🖼️ 图片 Image         | 列举与查询图片、本地路径 / 缩略图解析                        | 🟢     |
| 🌐 网页 Web            | 列举与搜索已同步网页、解析 Markdown 文件与存储目录路径       | 🟢     |
| 💡 灯效 Light         | 下发灯效指令到硬件（段 / 预设 / 规则三选一），连通性自检     | 🟡     |
| 📐 灯效规则 Lightrule | 「通知 → 灯效」持久规则的增删改查、启用 / 停用               | 🟡     |
| ⏰ 监控 Monitor       | cron 驱动的定时通知监控任务                                  | 🟡     |
| 🔌 隧道 Tunnel        | Relay 隧道状态、强制重连、本地 ingest 回环自检               | 🟡     |
| 🛡️ 网关 Gateway       | 模拟手机端调 daemon，校验本地连通与鉴权                      | 🟢/🟡  |
| 📋 日志 Log           | daemon 日志检索与 error 级筛选                               | 🟢     |
| ⚙️ 基础设施           | config / profile / auth / daemon / migrate / update / doctor / uninstall | 🟢/🔵  |
| 🧩 技能 Skills        | 把随包 SKILL.md 安装到 Agent 可发现目录                      | 🟢     |

> daemon 标记：🟢 不需要 daemon · 🟡 需要 daemon 在跑 · 🔵 管理 daemon 自身。

## 安装与快速开始

两种分发渠道，二者**功能一致**，按需选择。npm 包现在是极薄 Node launcher，真正执行的是 optionalDependencies 安装的 Go 原生二进制；直接安装渠道则跳过 Node，直接下载同一套 Go binary。

**平台支持**：npm 渠道支持 `darwin/linux` 的 `x64+arm64` 与 `win32-x64`（launcher 需 Node ≥ 18）；`install.sh` / GitHub Release 直装支持 `darwin/linux` 的 `x64+arm64`。Windows 上凭据以明文落 `~/.yoooclaw/credentials.json`（无系统 keychain 加固，`yoooclaw doctor` 会提示），daemon 停止经 HTTP 优雅退出。

### 渠道 A — npm（薄 Node launcher + 平台 Go 二进制）

```bash
# 免安装（每次拉最新版）
npx @yoooclaw/cli --help

# 全局安装（提供 yoooclaw / yc 两个命令）
npm i -g @yoooclaw/cli
yoooclaw --help
yc --help
```

### 渠道 B — 原生二进制（无需 Node）

单文件 Go 可执行，冷启动和资源占用都比旧 TS/Bun 形态更轻。

```bash
# 自动检测平台、下载、校验 sha256、写到 ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh | sh

# 如需安装器自动把 bin 目录加入 shell PATH，请显式开启
curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh \
  | sh -s -- --modify-path

# 指定版本 / 安装目录 / 覆盖
curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh \
  | sh -s -- --version 0.2.0-beta.2 --dir ~/bin --force
```

安装器默认不会修改 shell 启动文件；只有显式传入 `--modify-path` 才会写入 PATH。
`--no-modify-path` 仍保留，用于兼容已有调用。

直装支持平台：`darwin-arm64` / `darwin-x64` / `linux-x64` / `linux-arm64`。Windows Go binary 目前随 npm 平台子包发布。也可从 [GitHub Releases](https://github.com/YoooClaw/cli/releases?q=cli-v) 手动下载（同 release 内 `checksums.txt` 校验）。

> `yoooclaw update self` 会按当前安装来源给出对应升级命令（npm 走 `npm update -g`，二进制走 install.sh）。

### 快速开始（人类用户）

```bash
# 1. 交互式首次向导：生成 config + gateway token，并自动拉起 daemon
yoooclaw config init

# 2. 查鉴权与环境是否就绪
yoooclaw auth status
yoooclaw doctor

# 3. 开始用：纯读磁盘查通知，不需要 daemon
yoooclaw notification +today
```

### 快速开始（AI Agent）

> 以下步骤面向 AI Agent，部分需用户在终端确认。查询类命令纯读磁盘即可，控制类命令需 daemon 在跑。

```bash
# 1. 用 stdin 注入配置完成初始化（避免交互），随后自动起 daemon
yoooclaw config init --non-interactive --from-file -

# 2. 确认 daemon 在跑、隧道已连
yoooclaw daemon status
yoooclaw tunnel status

# 3. 流式查询通知（基于磁盘最新数据，勿依赖记忆）
yoooclaw notification summary --app 微信 --from 2026-06-01T00:00:00+08:00 --format json

# 4. 把随包 Skill 装到当前 Agent 的发现目录
yoooclaw skills install
```

## Agent 技能

> **推荐搭配 [YoooClaw/skills](https://github.com/YoooClaw/skills) 一起用。** 该仓库分发的 `yoooclaw-cli` Skill 把命令路由、`--format` 输出契约、daemon 依赖与错误处理打包成一份给 Agent 的「使用说明书」，让 Codex / Claude Code 等开箱即懂怎么调 `yoooclaw`。先装 CLI，再装 Skill：

```bash
# 1. 安装 CLI（前置）
npm install -g @yoooclaw/cli

# 2. 安装 yoooclaw-cli Skill —— Codex + Claude Code
npx skills@latest add YoooClaw/skills --skill yoooclaw-cli --global --agent codex --agent claude-code --copy --yes

# 只装其中一个 agent：把 --agent 改成单个即可
npx skills@latest add YoooClaw/skills --skill yoooclaw-cli --global --agent claude-code --copy --yes
```

> Hermes Agent 用 `hermes skills install https://raw.githubusercontent.com/YoooClaw/skills/main/yoooclaw-cli/SKILL.md`。装好后重启 Agent 会话即可被发现。

### 本仓库随包内置 Skill

随包发布 [skills/](skills/) 下的多个 Skill，教 Agent 直接调用 `yoooclaw`。运行 `yoooclaw skills install` 会把二进制内嵌的 Skill 复制到 Agent 的发现目录。

| Skill                           | 说明 |
| ------------------------------- | ---- |
| `yoooclaw-context-query`        | 查询最新通知、语音输入、录音/转写、已抓取网页、同步图片及跨来源本地上下文的唯一查询 Skill |
| `yoooclaw-recordings-process`   | 用一套录音来源流程路由会议纪要、翻译、思维导图、采访整理和实体提取 |
| `yoooclaw-lightrule-create`     | 通过独立 CLI 创建和管理「通知 → 灯效」持久规则；CLI 包没有 Agent 灯效规则工具，因此继续保留 |
| `yoooclaw-tunnel-debug`         | 排查鉴权、daemon、ingest、Relay WebSocket 与手机同步链路（🟡） |

```bash
yoooclaw skills list                 # 列出随包发布的内置 Skill
yoooclaw skills targets              # 查看支持的 Agent 目标和探测结果
yoooclaw skills install              # 自动探测唯一 Agent 后复制安装
yoooclaw skills install --agent claude
yoooclaw skills install --force      # 刷新内置 Skill，并清理已合并的旧名称
```

Skill 内嵌在原生二进制中，安装时复制。升级 CLI 后重新运行 `yoooclaw skills install --force`，再重启 Agent 会话。

## 鉴权

yoooclaw 的鉴权围绕两类凭据：**api-key**（account 级，签名手机端上行 ingest）与 **gateway token**（本地 daemon HTTP 鉴权）。多数命令为本地检查（🟢），`auth check` 会端到端调 daemon（🟡）。

| 命令                               | 说明                                                     |
| ---------------------------------- | ---------------------------------------------------------- |
| `auth set-api-key <key>`           | 设置/轮换 account 级 default api-key（`-` 从 stdin 读）  |
| `auth add-api-key <key>`           | 新增一条 multi-key api-key，可带 `--label` / `--default` |
| `auth list-api-keys`               | 列出 api-key 条目（key 自动遮罩）                        |
| `auth set-default-api-key <label>` | 切换 default api-key                                     |
| `auth remove-api-key <label>`      | 删除指定 label 的 api-key                                |
| `auth token-rotate`                | 生成新 gateway token；daemon 在跑时随后 restart 生效     |
| `auth status`                      | 显示鉴权状态（本地检查，不调 daemon）                    |
| `auth check`                       | 端到端鉴权体检（调 daemon `/daemon/status`）             |

```bash
# 从 stdin 安全写入 default api-key，并存入 OS keychain
echo "ock-xxxx" | yoooclaw auth set-api-key - --keychain

# 多设备/多客户端：按 label 管理多条 api-key
yoooclaw auth add-api-key - --label phone-a --default
yoooclaw auth list-api-keys

# 轮换 gateway token 后让 daemon 生效
yoooclaw auth token-rotate
yoooclaw daemon restart
```

## Daemon 开机自启

初始化 active profile 时，默认注册用户登录自启并立即启动 daemon：macOS 使用
launchd，Linux 使用 systemd user service，Windows 使用计划任务。整个流程不安装
系统级服务，也不需要管理员权限。

```bash
yoooclaw daemon autostart status
yoooclaw daemon stop                 # 只停止本次登录，自启保持开启
yoooclaw daemon start                # 立即启动，不改变自启偏好
yoooclaw daemon autostart disable    # 立即停止，并关闭以后登录自启
yoooclaw daemon autostart enable     # 开启自启并立即启动
yoooclaw daemon logs --supervisor    # 查看系统服务启动失败日志

yoooclaw config init --no-start      # 开启自启，但当前不启动
yoooclaw config init --no-autostart  # 当前启动，但不开启自启
```

自启服务始终跟随 active profile；`profile use` 会在 profile 间转移正在运行的
daemon，但会保留用户手动 stop 后的停止状态。Linux 默认随用户服务管理器启动；是否
通过 `loginctl enable-linger` 扩展为登录前启动，仍由系统管理员显式决定。

## Daemon lifecycle protocol

`yoooclaw daemon` exposes lifecycle metadata so external orchestrators such as
the Hermes plugin can keep the daemon and their websocket connections in the
same generation.

```bash
yoooclaw daemon run-foreground --owner hermes-plugin --generation abc123
yoooclaw daemon stop --owner hermes-plugin --generation abc123 --wait
yoooclaw daemon status --format json
```

`daemon status` includes `pid`, `version`, `executable`, `profile`,
`relay.env`, and a nested lifecycle object:

```json
{
  "ok": true,
  "version": "0.3.0",
  "executable": "/Users/me/.yoooclaw/hermes-plugin/bin/0.3.0/yoooclaw",
  "profile": "test",
  "lifecycle": {
    "owner": "hermes-plugin",
    "generation": "abc123",
    "startedAt": "2026-06-18T10:34:07Z"
  }
}
```

The lifecycle flags are optional; normal human `daemon start`, `stop`, and
`restart` usage remains unchanged. When supplied to `daemon stop`, owner and
generation are checked before the process is terminated.

### Ingress 模式（daemon 连接可选、可代理）

「到手机的连接」分层后可由 `--ingress` 选择**唯一** owner，避免独立 CLI 与宿主插件
（如 hermes-plugin）同时连 Relay 导致双连接、双 ingest。优先级 `--ingress` flag >
`YOOOCLAW_INGRESS` 环境变量 > `config.ingress.mode`，默认 `standalone`。

| 模式 | 到手机的连接 owner | Relay 隧道 | ingest 鉴权 | 出站事件 |
| --- | --- | --- | --- | --- |
| `standalone`（默认） | Go daemon 自己的隧道 | 启用 | gateway token / 本机 | 经 Relay 推回手机 |
| `proxied`（嵌入插件） | 宿主插件代理 | **关闭** | **必须 api-key** | POST 回宿主回调 URL |
| `direct`（LAN / 测试） | 调用方直接 POST | 关闭 | api-key / token | 丢弃（仅落盘） |

`proxied` 下 daemon 不连隧道，只暴露 ingest API（`POST /notifications` `/recordings`
`/images`，带 `Authorization: Bearer <api-key>`）供宿主把手机数据喂进来；出站事件
（如 `recording.status`）经 `--egress-callback-url` 回投宿主，再由宿主转发手机。

```bash
# 嵌入宿主：关掉 Go 自身隧道，让宿主代理连接并接收回投事件
yoooclaw daemon run-foreground --ingress proxied \
  --egress-callback-url http://127.0.0.1:8765/yoooclaw/egress \
  --egress-callback-token <token>
```

`daemon status` 输出新增 `ingressMode` 字段。完整分层设计见
[docs/design/ingress-layering.md](docs/design/ingress-layering.md)。

## 三层命令体系

按粒度从快捷到完全自定义，覆盖日常操作到任意 daemon 端点：

### 1. Shortcuts

以 `+` 前缀，对人类与 AI 都友好，自带智能默认值与表格输出。

```bash
yoooclaw notification +today          # 今日通知摘要
yoooclaw notification +recent         # 最近 1 小时通知
yoooclaw recording +latest            # 最新一条录音详情
yoooclaw recording +today             # 本地自然日内的今日录音
yoooclaw voice +latest                # 最新一条已保存的语音输入
yoooclaw voice +today                 # 本地自然日内的语音输入历史
yoooclaw light +blink                 # 灯效连通性测试（red-strobe-3）
yoooclaw lightrule +on                # 启用所有灯效规则
yoooclaw tunnel +test                 # daemon 本地 ingest + 鉴权自检
yoooclaw log +errors                  # 昨天起的 error 级日志
```

运行 `yoooclaw <service> --help` 查看某 service 的全部快捷命令。

### 2. Service Commands

`yoooclaw <service> <subcommand> [...flags]`，结构化访问各领域能力，service 列表见 `yoooclaw --help`。

```bash
yoooclaw notification search --app 微信 --keyword 会议 --limit 50
yoooclaw notification stats --dim app --from 2026-05-26
yoooclaw notification summary-job create --from 2026-06-01T00:00:00+08:00 --chunk-size 150  # 大批量通知分片总结：create→next→commit→result
yoooclaw recording list [--source all|capture_app|smart_hardware] [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>]
yoooclaw recording setup-asr --mode api --language auto --non-interactive
yoooclaw recording setup-asr --mode api --language zh-TW --non-interactive   # 繁体中文 / 台湾语境提示
yoooclaw recording setup-asr --mode api --language zh-Hant --non-interactive # 繁体中文脚本提示
yoooclaw voice list [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>]
yoooclaw voice apps                                # 最近 7 天去重后的 App ID/名称
yoooclaw voice search "项目" --app com.microsoft.VSCode
yoooclaw synced-web-page list [--from <ISO_TIME>] [--to <ISO_TIME>]
yoooclaw synced-web-page search "JavaScript" --limit 20
yoooclaw synced-web-page path <url-hash>
yoooclaw synced-web-page storage-path
yoooclaw lightrule create --intent "老板发微信时红灯快闪"   # 云端 Agent 编译并保存规则
yoooclaw monitor create daily-standup --schedule "0 9 * * 1-5" --match-rules '{"keyword":"standup"}'
```

`synced-web-page list` 按 `capturedAt` 过滤索引：`--from` 包含起点，
`--to` 不包含终点；时间参数使用带明确时区的 ISO 8601 格式。

ASR 语言提示中，`auto` 表示保留 provider 侧自动识别；台湾繁体中文场景可使用 `zh-TW`，仅需指定繁体中文脚本时可使用 `zh-Hant`。

不带任何筛选条件的 `voice list` 默认返回滚动最近 72 小时，并在 JSON 中明确提示；查询全部已保存历史请使用 `voice list --all`。命令没有默认条数上限。
`voice apps` 默认查询滚动最近 7 天，按 `app_id` 去重并同时返回实际的 `app_id` 与
`app_name`；显式查询全历史 App 清单请使用 `voice apps --all`。
Voice 历史从每日 `audio-jsonl/YYYY-MM-DD.jsonl` 读取，对外 ID 是稳定的字符串
`voice_id`，不会回退读取旧 SQLite。录音查询默认同时覆盖 `YoooClaw Capture`
（`capture_app`）和 YoooClaw 智能硬件（`smart_hardware`），每条结果都会标明来源。

### 3. Raw API

`yoooclaw api <METHOD> <PATH> [--data ...]` 直达 daemon HTTP 端点，覆盖未被 service 命令封装的部分。

```bash
yoooclaw api GET /daemon/status
yoooclaw api POST /images --data @image.json
echo '{"...":"..."}' | yoooclaw api POST /recordings --data -
```

## 进阶用法

### 全局 flags

| flag               | 说明                                                            |
| ------------------ | ------------------------------------------------------------------ |
| `--profile <name>` | 切换 profile（默认 `default`）                                  |
| `--format <fmt>`   | `json\|pretty\|table\|ndjson`（TTY 默认 pretty，管道默认 json） |
| `--quiet`          | 抑制进度日志，只输出最终结果                                    |
| `--no-color`       | 关闭终端颜色                                                    |

### 输出格式

```bash
--format json      # 完整 JSON（管道默认）
--format pretty    # 人类友好的格式化输出（TTY 默认）
--format table     # 可读表格
--format ndjson    # 行分隔 JSON，便于逐条管道处理
```

### 输出契约

成功与失败共用同一通道（stdout）与可预测结构。本地 CLI 校验 / 运行时错误会额外以非零退出码表达；`api` 这类 Raw HTTP 命令会尽量保留 daemon 原始响应，脚本里应同时检查 `ok` 与 HTTP status：

```json
{
  "ok": false,
  "error": {
    "code": "YOOOCLAW_DAEMON_NOT_RUNNING",
    "message": "...",
    "hint": "..."
  }
}
```

错误码统一前缀 `YOOOCLAW_*`（见 [internal/errs/errors.go](internal/errs/errors.go)）。

### 多 profile

`--profile <name>` 在不同账号/设备间切换，数据隔离在 `~/.yoooclaw/profiles/<profile>/`。

```bash
yoooclaw profile list
yoooclaw profile create work
yoooclaw --profile work notification +today
```

### 录音与 Relay

独立 daemon 用 Go 版录音存储、状态机、OSS 下载与 ASR 调度，并通过 `RelayClient + RelayDispatcher` 接收 App/云端的 `recordings.result.write`（写入转录/总结，可选下载音频）。
新录音请求应携带 `recording.created_at`（也兼容顶层 `createdAt` / `created_at`）；`transcript.generatedAt` 只表示转写产物生成时间，不再充当录音时间。
顶层 `durationMillis` 向下截断为整数秒，并以数值字段 `duration_sec` 保存；`duration_display` 提供 `16s`、`1m 16s`、`1h 1m 1s` 等带单位展示。音频下载落盘后，CLI 按兼容 Android `Formatter.formatShortFileSize()` 的 SI 短格式保存 `file_size_display`；已删除的 `file_size_bytes` 不再输出。

```bash
yoooclaw recording events --since 1h --limit 50
yoooclaw recording events --id <recording-id> --watch
```

录音配置与事件分别落在当前 profile 的 `recordings/asr-config.json` 与 `recordings/state/events.jsonl`。

### 数据目录

`~/.yoooclaw/`（可用 `YOOOCLAW_HOME` 覆盖，便于测试 / 多实例）。布局见 [internal/paths/paths.go](internal/paths/paths.go) 与 PRD「数据模型」。

## 安全与风险提示

本工具会被 AI Agent 调用来自动化操作本地 daemon 与手机端链路，存在模型幻觉、不可预期执行与提示注入等固有风险。授权后 Agent 将在你的身份与授权范围内行事，可能导致敏感数据泄露或非预期操作，请谨慎使用。

为降低风险，工具默认开启多层防护：daemon 仅监听本地端口、本地 ingest 经 gateway token 鉴权、凭据优先存入 OS keychain、终端输出对敏感字段遮罩。**强烈建议不要主动放宽这些默认安全设置**；一旦放宽，风险将显著上升，后果需自行承担。请充分理解全部使用风险——使用本工具即视为自愿承担相关责任。

## 开发与贡献

```bash
go test ./...
go vet ./...
scripts/build-go.sh --current
dist-native/yoooclaw-darwin-arm64 --help
```

完整文档见 [yc-docs/src/cli](https://github.com/YoooClaw/yc-docs/tree/master/src/cli)。欢迎提 Issue / PR；较大改动建议先开 Issue 讨论。

### 源码结构

| 文件 / 目录                                         | 职责 |
| ---------------------------------------------------- | ---- |
| [cmd/yc/main.go](cmd/yc/main.go)                    | Go binary 入口 |
| [internal/cli/root.go](internal/cli/root.go)        | cobra root、全局 flags、service 命令接线 |
| [internal/cli/handler.go](internal/cli/handler.go)  | handler 包装、输出与错误渲染 |
| [internal/output/output.go](internal/output/output.go) | `--format` 统一序列化 |
| [internal/errs/errors.go](internal/errs/errors.go)  | `YOOOCLAW_*` 错误码 |
| [internal/paths/paths.go](internal/paths/paths.go)  | `~/.yoooclaw/` 目录布局解析 |
| [internal/daemon/server.go](internal/daemon/server.go) | daemon HTTP server、鉴权、Relay 装配 |
| [internal/daemon/server_ingest.go](internal/daemon/server_ingest.go) | notifications / recordings / images ingest |
| [internal/relay/dispatcher.go](internal/relay/dispatcher.go) | Relay 入站帧到 daemon HTTP/gateway 的进程内分发 |
| [internal/recording](internal/recording)            | 录音 OSS 下载、状态机、ASR、转写稿存储 |
| [internal/capturerecording](internal/capturerecording) | YoooClaw Capture 录音每日索引与产物事实检查 |
| [internal/voice](internal/voice)                    | 本地语音输入每日 JSONL 历史只读查询 |
| [internal/image](internal/image)                    | 图片 OSS 下载与索引 |
| [internal/light](internal/light)                    | 灯效线协议、预设、发送器 |
| [internal/skills](internal/skills)                  | 内置 Skill 列举 / 安装到 Agent skills 目录 |

发布产物全部由 Go 代码经 `scripts/build-go.sh` 生成（npm 平台子包 + 原生二进制）；早期的 TypeScript 实现已下线，历史可在 git 记录中查阅。

## License

MIT —— 见 [LICENSE](LICENSE)。

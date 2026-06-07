# @yoooclaw/cli

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
- **纯读磁盘的查询** —— 通知 / 录音 / 图片查询直接读 `~/.yoooclaw`，不需要 daemon 在跑
- **统一输出契约** —— `--format json|pretty|table|ndjson`，成功失败同通道、结构可预测；本地 CLI 错误返回非零退出码，Raw/daemon HTTP 响应请同时检查 `ok` / HTTP status
- **凭据安全** —— OS keychain 优先存储，多 api-key 管理，gateway token 鉴权本地 ingest
- **Go 原生二进制** —— npm 薄 launcher + 平台子包，或直接安装原生 binary；macOS / Linux / Windows 全平台

## 能力一览

| 领域                  | 能力                                                         | daemon |
| --------------------- | ------------------------------------------------------------ | ------ |
| 📱 通知 Notification  | 按时间/应用/发送人/关键词查询，今日/最近摘要，多维聚合统计   | 🟢     |
| 🔄 同步 Sync          | 扫描未处理通知、按日期取详情、提交批次，供记忆系统消费       | 🟢     |
| 🎙️ 录音 Recording     | 列举与查询录音、ASR 转写配置（api/model-proxy；local 已停用）、状态事件流跟随 | 🟢     |
| 🖼️ 图片 Image         | 列举与查询图片、本地路径 / 缩略图解析                        | 🟢     |
| 💡 灯效 Light         | 下发灯效指令到硬件（段 / 预设 / 规则三选一），连通性自检     | 🟡     |
| 📐 灯效规则 Lightrule | 「通知 → 灯效」持久规则的增删改查、启用 / 停用               | 🟡     |
| ⏰ 监控 Monitor       | cron 驱动的定时通知监控任务                                  | 🟡     |
| 🔌 隧道 Tunnel        | Relay 隧道状态、强制重连、本地 ingest 回环自检               | 🟡     |
| 🛡️ 网关 Gateway       | 模拟手机端调 daemon，校验本地连通与鉴权                      | 🟢/🟡  |
| 📋 日志 Log           | daemon 日志检索与 error 级筛选                               | 🟢     |
| ⚙️ 基础设施           | config / profile / auth / daemon / migrate / update / doctor | 🟢/🔵  |
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

# 指定版本 / 安装目录 / 覆盖
curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh \
  | sh -s -- --version 0.2.0-beta.2 --dir ~/bin --force
```

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

随包发布 [skills/](skills/) 下的 SKILL.md，教 Agent 直接调 `yoooclaw` 命令。在 openclaw 插件里由 `openclaw.plugin.json` 自动注册；独立 CLI 形态下用 `yoooclaw skills install` 软链到 Agent 的 skills 发现目录。

| Skill                         | 说明                                                                                                            |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `yoooclaw-notification-query` | 流式查询手机通知原始数据：「看看最近的通知」「谁找过我」「总结今天的消息」。纯读磁盘、不需要 daemon             |
| `yoooclaw-lightrule-create`   | 从自然语言或 stdin 创建/管理「通知 → 灯效」持久规则，由 daemon 在 ingest 后评估命中并触发灯效（🟡）             |
| `yoooclaw-tunnel-debug`       | 排查手机端推送链路：组合 auth / daemon / tunnel / gateway 状态定位本地配置、ingest 鉴权与 Relay WebSocket（🟡） |

```bash
yoooclaw skills list                 # 列出随包发布的内置 Skill
yoooclaw skills targets              # 查看支持的 Agent 目标和探测结果
yoooclaw skills install              # 自动探测唯一 Agent 后软链安装
yoooclaw skills install --agent claude
yoooclaw skills install --copy       # 复制而非软链（Windows 无管理员权限时用）
```

默认软链而非复制：`yoooclaw update self` 升级 CLI 后 Skill 内容自动跟随新版本。安装后重启 Agent 会话即可被发现。

## 鉴权

yoooclaw 的鉴权围绕两类凭据：**api-key**（account 级，签名手机端上行 ingest）与 **gateway token**（本地 daemon HTTP 鉴权）。多数命令为本地检查（🟢），`auth check` 会端到端调 daemon（🟡）。

| 命令                               | 说明                                                     |
| ---------------------------------- | -------------------------------------------------------- |
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

## 三层命令体系

按粒度从快捷到完全自定义，覆盖日常操作到任意 daemon 端点：

### 1. Shortcuts

以 `+` 前缀，对人类与 AI 都友好，自带智能默认值与表格输出。

```bash
yoooclaw notification +today          # 今日通知摘要
yoooclaw notification +recent         # 最近 1 小时通知
yoooclaw recording +latest            # 最新一条录音详情
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
yoooclaw recording list --status synced
yoooclaw recording setup-asr --mode api --language auto --non-interactive
yoooclaw lightrule create --from-file -          # 从 stdin 提交规则定义
yoooclaw monitor create daily-standup --schedule "0 9 * * 1-5" --match-rules '{"keyword":"standup"}'
```

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
| ------------------ | --------------------------------------------------------------- |
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

独立 daemon 用 Go 版录音存储、状态机、OSS 下载与 ASR 调度，并通过 `RelayClient + RelayDispatcher` 接收手机端 `recordings.sync` / `POST /recordings`。

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
| --------------------------------------------------- | ---- |
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
| [internal/image](internal/image)                    | 图片 OSS 下载与索引 |
| [internal/light](internal/light)                    | 灯效线协议、预设、发送器 |
| [internal/skills](internal/skills)                  | 内置 Skill 列举 / 安装到 Agent skills 目录 |

发布产物全部由 Go 代码经 `scripts/build-go.sh` 生成（npm 平台子包 + 原生二进制）；早期的 TypeScript 实现已下线，历史可在 git 记录中查阅。

## License

MIT —— 见 [LICENSE](LICENSE)。

# 独立 CLI 环境切换与灰度更新实施计划

日期：2026-10-08。负责人：杨学鑫。状态：本地实现及自动化验证已完成，尚未发布；测试区真实升级联调待完成。

## 已有文档与本期范围

共用服务端方案位于 software 仓库 `projects/plugin-rollout/`：`requirements.md`、`technical-design.md` 和 `contracts/`。本文补充 CLI 实现范围；服务端字段以联调确认的契约为准，不另建接口。

本期按顺序实现：

1. CLI 环境切换与实际环境展示，支持测试区联调。
2. 更新过程日志上报。
3. 独立 CLI 灰度检查和后台更新；检测到 Claude 时更新本产品的 skill。

明确不做：CLI 与 Hermes 分别保存环境、凭证拆分、修改现有 API Key、多 Key 归属迁移、不同环境并行运行、Hermes 内置 CLI 自更新。继续保留共享环境及现有 owner 互斥。

## 一、环境切换

### 当前实现

- CLI 已支持 development、test、production；缺少专门的环境切换命令。
- CLI 主机优先级：显式环境变量 → cloud.host → 内置 relay.url 主机 → 默认生产。
- Hermes 也读取共享 profile，但部分环境解析来自 relay.url；只修改 cloud.host 会造成两端判断不同。
- 两边默认共用 `~/.yoooclaw/active-profile`、profile 配置和账号凭证。

### 拟新增命令

```sh
yc env show
yc env switch test
yc env switch production
yc env switch development
```

命名与 OpenClaw 插件现有的 `openclaw ntf env show` / `openclaw ntf env switch <env>` 保持一致，使用 `switch` 而非 `use`；参数统一为 production、test、development。本分支已实现该语法，尚未发布。Hermes 保留原有 `hermes yoooclaw env <profile>` 入口，本期不替换它。

命令修改当前选中的 profile，不创建按 owner 区分的环境配置，不自动修改 active profile。显式选择非活动 profile 时，只修改该 profile，不能误重启其他 profile 的 daemon。

### 行为

| 当前运行 owner | CLI 切环境行为 |
| --- | --- |
| CLI | 修改目标 profile；若对应 daemon 正在运行，重启并验证实际连接 |
| Hermes | 写配置前拒绝，提示使用 Hermes 的环境命令；不自动接管 |
| 无 owner | 保存配置，不自动启动 daemon |
| 无法确认 | 报错退出，不写配置 |

复用真实 writer/Relay 锁和存活状态判断 owner，不能用“CLI daemon 不在线”推断无 owner。切换与现有接管需有一致的互斥保护，避免判断后 owner 改变；仅在新命令中加锁而接管不遵守同一把锁不足以解决竞态。

切换时同步当前 profile 的 cloud.host 与内置 relay.url，保持 CLI、Hermes 对共享环境的解释一致。对于自定义 Relay URL，不静默覆盖为公共地址，应明确提示需先处理自定义配置。

显式 PHONE_NOTIFICATIONS_ENV / OPENCLAW_HOST_* 与目标冲突时，在写入前说明覆盖来源并拒绝，不通过改 shell 配置或删除用户变量来解决。还需检查托管 daemon 的启动环境，防止重启后仍被旧变量覆盖。

不修改 Key、不自动选择其他 Key；鉴权失败明确报告目标环境和失败状态，不悄悄切回别的环境。配置保存与连接恢复分别报告，重启或鉴权失败不能返回“切换全部成功”。不在失败后无提示地启动另一 owner。

有录音下载、同步写入等进行中任务时，拒绝本次重启并提示稍后重试，避免为切环境打断同步；执行前核实运行态检查覆盖的任务范围。

输出至少包含 profile、owner、配置主机、实际连接主机、环境来源、连接状态。无进程时实际连接信息为空。切 owner 后仍继承共享环境，命令结果需明确展示这一行为，不承诺两边环境独立。

### 涉及模块

- `internal/cli/`：新增环境命令及注册，复用 daemon/owner 控制逻辑。
- `internal/config/`、`internal/envhost/`：统一从有效主机推导环境；自定义主机标为 custom，不伪报 production。
- `internal/daemon/server.go`：修复状态直接使用 envhost.Name() 导致环境名与实际域名不一致的问题。
- `internal/autostart/`：核实托管进程环境与重启行为，保持现有自启策略。
- README 与必要的相关 skill 文档：补充命令、owner 限制及共享环境说明。

## 二、更新日志上报

沿用插件上报协议与阶段语义，补全 CLI 官方更新入口和独立安装进程的上报。使用实际生效环境选择服务地址；不把 beta 版本号当成测试环境。

- 每次更新生成 updateId，检查、下载、校验、安装、重启和生效确认共用；单条日志使用 reportId。
- 安装进程退出不等于新版生效；由新版 daemon 启动后确认运行版本。
- 安装器早期失败、启动失败、鉴权失败在可记录处写本地日志；服务鉴权拒绝时不能保证云端收到。
- 未成功发送的事件持久化后补报，队列有界，遵循协议重试规则；上报失败不阻断正常更新。
- API Key 用于接口鉴权，但本地日志、错误信息与 logTail 在写入时脱敏；不依赖云端补做脱敏。
- 复用已有 updateId 关联阶段日志与最终结果。服务端幂等以最新契约为准。

## 三、灰度更新与 skill

- 独立 CLI 使用 component=CLI；实际请求统一包含 `/api` 前缀。服务端识别账号、手机号白名单及灰度资格，客户端不上传自报手机号或 userId。
- daemon 启动约一分钟后检查，此后每四小时检查，服务端据此记录活跃。
- 下载前及安装前复查指定目标版本的资格；平台返回 OSS 下载地址，CLI 直接下载制品并校验版本、大小和 SHA-256。
- 遵守平台允许的更新时间策略。已知忙碌、录音下载、同步或写入进行中就延后；拿不到 Agent 状态时按连续十分钟无用户消息判断，并保护 CLI 自身进行中的任务。
- 安装前检查忙碌状态，不以停止进程后等待排空作为正常更新策略；验证重启后未完成同步的恢复能力。
- 复用既有 daemon 启停和 skill 安装能力，使用独立进程替换原生二进制，保持现有环境、凭证和自启行为。后台更新不得传入接管选项或触发 Hermes 接管/重启；无影保留既定自启行为。
- 手动更新不依赖 daemon 在线；同一安装目标的并发更新互斥。
- Hermes 内置二进制、shim 和可能共享的外部 CLI 不进入独立 CLI 后台替换范围；不能只靠 PATH 或没有 Hermes 进程判断归属。
- 检测到 Claude 后更新本产品的 skill，沿用现有 skill 安装逻辑，不覆盖无关 skill。skill 更新结果单独报告，失败不能伪报成功或掩盖 CLI 已更新的事实。
- 多 Key 复用既有默认 Key 解析结果决定灰度资格，不对所有 Key 的资格取并集；运行期切 Key 或环境会更换队列分区，旧队列不能发到新账号或环境。
- Windows 已实现独立 worker、主程序和 yc.exe 别名替换；交叉编译通过，仍需 Windows 实机验证文件占用和系统服务重启。

## 四、验证与已知问题

### 本机已观察到的现象

2026-10-08，在 Hermes 0.10.0、CLI 0.12.0-dev.6578b60 上实际执行 Hermes → CLI → Hermes：

- CLI 接管成功，但跳过标签 legacy 的 Key，仅一条隧道连接；Hermes 可连接两条。CLI 标签规则与 Hermes 安装器写入规则不一致。本文记录该独立问题，不在环境切换中顺带迁移或修改 Key。
- CLI 实际连接测试域名，状态中的 cloud.env、relay.env 却显示 production；纳入本期修复。
- 切回 Hermes 遇到 launchd 自动恢复等待及 kickstart 延迟，最终恢复两条隧道。宿主提示服务定义过期，是否为延迟根因尚未确认。
- 测试后已恢复 Hermes owner、测试环境、两条连接；未验证本次切换期间的录音补传。

### 实现后必须验证

1. 三环境切换与 daemon 重启后的实际域名一致；环境覆盖冲突、自定义域名不静默误切。
2. Hermes owner 下 CLI 切换拒绝且配置不变；无 owner 不意外启动进程；接管竞态不误重启。
3. 显式非活动 profile 不影响当前 owner；切换 owner 后展示继承的共享环境。
4. 所有环境命令均不改变凭证内容；录音忙碌时不重启，空闲后可重试。
5. 白名单、未命中、暂停、资格失效、下载/校验/安装/重启失败、新版本真正生效及日志关联。
6. Hermes 内置 CLI 不被替换，Hermes 不被后台更新重启，录音同步恢复正常；Claude skill 更新失败可独立识别。

实施顺序：先完成并验证环境命令，再接上报和灰度。代码、服务联调及正式发版分别记录，不能以文档或单元测试通过代替真实升级验证。

## 五、与插件实现逐项对照

| 能力 | OpenClaw 插件现有实现 | CLI 对应实现和差异 |
| --- | --- | --- |
| 环境命令 | `src/cli/env.ts`：show、switch，保存后触发宿主重载 | `internal/cli/cmd_env.go`：同名 show、switch；保存共享 profile，遵守 owner 限制；只重启对应 daemon |
| 环境来源 | `src/env.ts`：插件持久化环境及覆盖 | 复用 CLI 的 config/envhost；不照搬插件的 .env 文件，不创建第二套环境来源 |
| 更新资格 | `src/update/ota.ts`：API Key 鉴权、统一响应校验、目标复查 | `internal/update/` 实现同等 API 客户端，component 改为 CLI；显式使用目标 profile 的有效主机 |
| 调度 | `checker.ts` 定时检查；`scheduler.ts` 闲时复查并执行，失败冷却四小时 | 接入 daemon 生命周期；启动一分钟、四小时检查、三十秒闲时复查；检查与安装状态分离，不在重试中反复重装 |
| 忙碌状态 | `activity.ts` 跟踪消息、Agent run 与录音任务 | CLI 增加运行中请求和录音后台任务计数；Agent 长任务来源需按 CLI 实际能力实现，不能假装拥有 OpenClaw hooks |
| 下载与安装 | `executor.ts` 校验包并交给独立安装器 | CLI 请求传 macos/linux/windows 与 arm64/x64，由服务端返回匹配二进制；不照搬插件 tgz 解包和包名校验 |
| 上报上下文 | `report/runtime.ts`：环境、Key 指纹、设备标识绑定，保存 pending-update | CLI 上下文绑定环境、默认 Key 指纹及实例；切环境/换 Key 不向新账号发送旧队列 |
| 阶段事件 | `report/outbox.ts`：reportId、updateId、stage、result、本地队列 | 保持 gate/download/verify/prepare/idle/apply/restart/activate/rollback/heartbeat 语义；只报告实际发生的阶段 |
| 队列边界 | 插件队列保留七天、最多二百条；每批最多五十条且请求不超过 64 KiB | CLI 按同等边界实现，原子写盘、单发送者、有限重试；不能将任意 HTTP 200 当确认 |
| 心跳与生效 | 插件启动检查 pending；heartbeat 每二十四小时，报告 runningVersion | CLI daemon 启动确认自身版本；仅文件替换成功不能报 activate succeeded；不恢复历史路径 installed.json |
| 最终结果 | `ota-result.ts`：同一 updateId 首个最终结果有效、持久化重试 | 沿用 updateId 关联；成功需新版运行确认，后端幂等字段依契约；网络失败不能生成第二次更新尝试 ID |
| Claude skill | 插件不负责独立 CLI 的 skill 分发 | 使用 CLI `internal/skills`，由新版本执行安装以取得新版内嵌资源；单独记录失败 |

### 更新状态流转

1. 检查：取得当前运行版本、默认 Key 和实际服务环境；后台四小时周期请求灰度资格。
2. 候选：保留版本及制品信息；无资格清除候选，空闲前不安装。
3. 准备：新建 updateId；精确复查候选、下载、校验并记录阶段结果。
4. 安装：再次复查目标资格、忙碌状态与安装归属；持久化上下文后由独立进程执行，不能让被替换 daemon 成为安装器的生命周期依赖。
5. 重启：保持原启动方式；失败记录 restart failed，不能只凭安装器退出码零报生效。
6. 生效：新版 daemon 检查目标版本、同一安装实例及更新上下文，记录 activate succeeded 并提交最终结果；清理临时交接状态，避免回退后误判。
7. 补报：无更新任务时也由运行时发送持久化队列；旧环境、旧账号队列不混发。服务端拒绝鉴权不能用不可信本地 userId 绕过。

### 制品协议：CLI 自动识别平台，服务端匹配单个制品

2026-10-09 按服务端最新确认，系统与架构合并为一个 `platform` 字段，服务端返回匹配 URL，不采用统一版本清单。该约定取代此前两个字段的方案；部署状态仍需联调确认。软件仓库接口说明已整理为 `projects/plugin-rollout/contracts/contracts.md`。

- CLI `component=CLI`，每次常规检查、下载前和安装前复查均传单个 `platform`，如 `linux-x64`、`linux-arm64`、`macos-arm64`、`windows-x64`，不再发送独立 `arch`。
- 组合值为 `<系统>-<架构>`；系统为 macos/linux/windows，架构为 arm64/x64。Go darwin 转 macos，amd64 转 x64。模拟运行时按当前可执行文件构建目标选择，不自行迁移架构。
- 响应字段保持 targetVersion/downloadUrl/fileSize/fileHash/releaseNotes，URL 指向匹配的单个制品；未匹配到合法组合返回 updateAvailable=false，不下载其他架构包。
- 缺少必填字段或枚举错误返回 OTA00004，不回退公共 latest。精确复查携带原平台/架构，不接受替换目标版本。
- OpenClaw 插件使用通用 tgz，检查请求不发送 `platform` 或 `arch`。阶段日志 `env.platform` 和 `env.arch` 保持现有格式。
- 已下载文件与安装前复查返回的大小/哈希必须一致；不根据 URL 猜测平台，不复用其他制品的哈希。
- 服务端通过 `platformArtifacts` 映射登记，如 `"linux-arm64": {"url":"…"}`，由发布流水线或管理员登记；只登记实际发布的组合。灰度平台为一个账号决定资格，平台/架构只参与制品匹配，不改变白名单分组。

### 当前实现进度

- 已实现环境 show/switch、实际主机环境展示、CLI 接管控制锁、请求和录音后台任务忙碌检查。
- 已实现带组合 platform 的灰度检查、默认 Key 鉴权、beta 数字比较、精确版本复查，以及单制品大小/SHA-256/运行版本校验。复查刷新下载 URL，但不能改变已选制品哈希或大小。
- 已实现四小时检查、三十秒空闲复查、十分钟无业务请求门槛、失败冷却四小时。自动安装使用既有 `autoUpdate.enabled`，默认开启；仅在 daemon 启动时读取，修改后需重启生效，保留检查/上报及手动安装。二次开发前建议关闭，避免官方版本覆盖自定义构建。不使用 autoUpdate.channel 绕过服务端资格。
- 已实现独立更新进程、安装互斥、替换失败回滚、保留自启方式、离线安装后的生效交接，以及 Claude skill 刷新。保留无影安装器写入 skill 的固定路径配置。
- 独立原生安装必须经新版官方脚本登记。Hermes 内置路径、npm 路径、进程显式 CLI override，以及 Hermes 主目录 .env 明确引用同一二进制时不替换。Hermes 自定义包装程序或其他配置中间接引用不属于自动识别范围。
- 已实现脱敏后本地队列、七天/二百条边界、五十条/64 KiB 批次、心跳二十四小时、updateId 关联最终结果和有限重试。阶段日志从安装开始写盘；运行时及 worker 负责补报。
- 官方安装脚本在已有 CLI 支持上报时使用其临时副本记录阶段，失败时不依赖正在被替换的主程序。首次安装或旧版本不支持上报时不能报告早期阶段；Wuying 外层接管步骤未单独接入阶段报告。官方显式安装不伪装成灰度授权尝试，不提交灰度最终结果接口。
- 接管控制锁只协调本期 CLI 命令；Hermes 未改用这把锁，不能宣称解决全部跨进程接管竞态。现有 Relay/写者锁仍为运行时归属依据。

### 本次本地验证记录

- 分支：`feat/0.13.0-environment-rollout`，2026-10-10 从远端 `release/0.13.0`（4359217）建立；该 release 包含最新远端 master（9201fbe）。原 `feat/cli-environment-rollout` 的本地修改已迁入，代码未提交、未推送。
- `go test ./...` 通过；update、skills、daemon、recording 的 `-race` 检查通过。
- Linux amd64、Windows amd64 交叉编译通过；不代表 Windows 安装/重启实机验证通过。
- 在临时 YOOOCLAW_HOME 中验证三环境切换；真实 OS 文件锁模拟 Hermes 占用时拒绝切换且配置与凭证不变。
- 本地 TLS 服务配合真实 Go 测试二进制执行文件替换：验证离线不自动启动、下次运行确认生效、交接记录失效后不因回滚复用。
- 模拟 runtime 控制回调验证忙碌不停止、资格撤销不安装、重启失败上报、运行版本/路径/连接确认后成功；没有把这些回调当成真实系统服务验证。
- 验证日志重试保持 reportId/updateId、首次最终结果不被改写、心跳二十四小时门槛和无影 skill 命令配置保留。
- 活动检查提供安装前快照，不冻结后续新任务；独立进程离线操作和外部 Agent 长任务不属于 daemon 请求计数。
- 未替换本机 CLI，未重启或切换当前 Hermes，未改变当前 Key；测试区平台真实制品、托管 daemon 重启/Relay 恢复、Windows 文件占用、录音恢复和服务端幂等仍需联调。



### 测试 CI 版本规则（2026-10-10）

日常测试发布统一生成 `<release 分支基础版本>-alpha.<GITHUB_RUN_NUMBER>`。例如 release/1.19.0-beta.1 的基础版本为 1.19.0，测试包为 1.19.0-alpha.123；不拼接 beta.1.alpha.123。流水线重跑版本不变，不使用 GITHUB_RUN_ATTEMPT。版本生成仅在 CI 工作区生效；生产 beta 与正式版流程不变。插件同步已有制品时保持制品原版本。

按 SemVer 同基础版本 alpha < beta < 正式版。已安装 beta 的设备不会自动降级到同基础版本 alpha，需显式安装测试包。服务端需先允许 alpha.N 的登记和比较；当前只接受 beta.N 时，插件草稿登记会明确失败，不视为完整发布成功。本次仅修改 CI，不触发发布。

### 2026-10-12 CLI alpha 发布准备

重新 fetch 后，origin/release/0.13.0 仍为 4359217，当前功能分支已包含该提交，merge --ff-only 返回 Already up to date，无新增冲突。当前灰度、环境切换和上报代码随测试包发布。

CI 测试发布生成 0.13.0-alpha.<run_number>，上传五种原生二进制并同步旧测试入口，然后用 test environment Secret 登记 CLI 草稿。平台键为 macos-arm64/macos-x64/linux-arm64/linux-x64/windows-x64，URL 指向原生二进制。后端计算大小和哈希；流水线不激活草稿。登记失败不自动重试创建。

本地 go test ./...、go test -race -coverprofile、go vet ./...、OSS 上传工具测试、登记脚本 3 项测试及 YAML/alpha 版本生成验证通过。总覆盖率 68.1%，门禁 55%。用户确认服务端已支持 alpha；真实平台登记仍以本次流水线结果为准。客户端测试区鉴权此前仍返回 000019，发布成功不代表客户端已完成更新联调。

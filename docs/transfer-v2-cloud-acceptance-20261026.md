# CLI v2 迁入：无影实机验收（2026-10-26）

## 结论

本机 HTTP 导入及 Claude 调用 Skill 导入已通过。六类记忆读取、资料位置索引写入及每日正文不读取已复测；定时任务迁移仍需托管会话完整验收；下载进度已接入并通过云电脑模拟履约测试（见文末补测）。不能据此宣称履约异步迁入链路已完成。

## 环境与范围

- 无影：Ubuntu 24.04 x86_64，admin 用户；Claude Code 2.1.220。
- CLI：本地 `codex/transfer-v2-import` 工作区编译 Linux 二进制，安装到 `/home/admin/transfer-e2e-20261026/yoooclaw-transfer-v2-linux`。
- 测试包装命令：`/home/admin/transfer-e2e-20261026/bin/yoooclaw`，固定 `transfer-e2e-20261026` profile。
- 测试 daemon：`127.0.0.1:19365`，direct 模式、不连接生产 Relay；默认 CLI 二进制和默认 daemon 未替换。
- 更新真实 `~/.claude/skills/yoooclaw-data-transfer`，原 Skill 已备份在验收目录。
- 云电脑仅余约 0.9 GB 空间，采用 22 个逻辑文件、11 类数据的小型人工测试包；没有执行 10 GB 压测。数据包含测试标记，不代表真实用户偏好。

## 导入结果

| 项目 | 实际结果 |
|---|---|
| HTTP `/transfer`：capabilities → preview → import | SUCCEEDED，新增 18 个结果项：4 条业务记录、14 个 workspace／备份项 |
| Claude 自然语言调用 Skill 导入同内容的另一个包 | 调用 Skill → 读 import.md → capabilities → preview → 执行 plan；SUCCEEDED，18 项均跳过，没有重复落盘 |
| 落盘完整性 | 14 个 workspace 文件逐个 SHA-256 与包清单匹配 |
| 录音摘要 | 按迁入后的真实 `summaryFile` 索引定位，校验标记正确；摘要文件名与录音标题不同 |
| cron 内容不同 | 保留原目标、隔离保存新定义，并将实际落点写入包索引 |
| 异步 `/transfer/import/start` | HTTP 404；未实现，不能将本机 `/transfer` 的通过等同履约／Relay 已接通 |

实测发现 cron 冲突副本退回 `jobs.json` 文件名，已修复为 `from_openclaw_cron.json`，补充回归测试并在无影重新导入验证。另明确 Skill 的计数口径：`counts` 包含 workspace，`workspaceCounts` 是其子集，不能再次相加或将总数称为业务条数。

## 熟悉与调度

用 Claude CLI 的 `-p --output-format stream-json --verbose` 发送自然语言提示词，审计真实工具调用及结果，而非只看模型总结。

- 初轮：六类源文件均读取，但模型额外读取了 `memory/2099-12-31.md`，随后错误声称未读取。工具日志中存在日报正文测试标记，该轮判失败。
- 修正 Skill：每次文件工具调用前检查目标；明确禁止以核对、调试、跟随 MEMORY.md 链接等理由打开每日日志；发生误读须如实报告。
- 复测：六类源文件读取、迁移位置记忆更新成功，没有每日文件 Read 调用，日志中也没有日报正文测试标记。历史会话和项目正文没有被批量阅读。
- 目标记忆更新为专用迁移笔记及既有 MEMORY.md 中的索引；测试内容被识别为验收数据，没有变成用户真实偏好。
- cron 实测：Claude 暴露 `CronCreate(durable:true)`，成功创建并通过 CronList 读回任务 `a1295769`，跨新 Claude 进程仍能列出。
- **但创建响应明确为 7 天自动过期**。落盘不代表可以无损恢复 OpenClaw 的长期任务；年末测试任务甚至会在首次触发前过期。因此不能判为任务恢复通过。
- 已补充 Skill 的生命周期检查：自动过期、需要活跃客户端、时区及投递都要匹配；不能用有限期任务冒充长期任务。如果创建回执才揭示不兼容，应清理本轮刚创建的任务并报告未恢复。

重复熟悉实测：新 Claude 进程能查询到原任务，没有再次调用 CronCreate，也没有读取每日正文；但仍错误宣称“任务忠实恢复”，忽略了已知 7 天有效期。因此只判去重与文件访问检查通过，长期任务恢复仍失败，不能按模型总结放行。进一步明确：CronList 中名称／表达式／载荷一致不足以证明生命周期兼容；缺失有效期证据时应标记未验证，已知 7 天过期应标记不兼容。

每日正文限制目前属于 Skill 执行指令，不是底层文件访问控制；本次复测通过不等于已加入强制拦截。

## 首轮下载进度模拟（接入前）

在云电脑运行由同一代码编译的验收探针：本地模拟文件服务签名 URL 接口和支持 Range 的对象存储，实际调用生产 `transfercloud.Client.Download`。

- 下载 17,600,000 字节，最终与源字节完全一致。
- 捕获 HTTP：1 次 `/api/device/file/plugin/download-url` 请求、4 次对象存储 GET。
- **首轮下载进度上报请求：0 次，当时未接进度监听／节流／履约回报；本轮已补齐客户端，见下文。**
- 探针执行成功只证明下载与检查过程完成，不代表进度上报测试通过；15 秒／1 个百分点的节流规则本轮无实现可验。

## 证据

云端 `/home/admin/transfer-e2e-20261026/` 保留接口预览／导入 JSON、校验结果、Claude 原始工具日志及审计 JSON、下载模拟日志。原始日志留在测试目录，不作为仓库正文提交。

相关实现与计划：[导入 IMP](transfer-v2-import-implementation-plan.md)、[熟悉规则](../skills/yoooclaw-data-transfer/references/familiarize.md)。

## 收尾与遗留

- Claude 已按 ID 删除测试任务 `a1295769`，再次 CronList 返回无任务；迁移记忆中的“忠实恢复”错误结论已改为长期恢复未通过。
- 独立测试 daemon 已停止；默认 daemon 仍运行。测试 CLI、profile、夹具与日志留存，便于核对；生产默认 CLI 未替换。
- 修正后的迁移 Skill 已装入真实 Claude Skill 目录，原版留有备份。最终 Linux 二进制包含最新 Skill。
- 本轮相关 Go 回归测试通过。云端新版本未发布到正式渠道，代码未提交／推送。
- 后续阻断项：在 runner 托管会话完整验收任务恢复；实现履约异步迁入入口、许可／结果事件及生产进度联调。不能仅靠提示词解决运行时接入缺口。


## 本轮补测：平台定时任务与下载进度接入

### 平台工具证据

只读取这台无影已有 Claude 工具调用记录，未重复创建用户的新闻任务。2026-10-26 08:44 UTC（北京时间 16:44）的实际记录显示：

- 工具：`mcp__ac_scheduled_tasks__scheduled_task_create`；任务“每天早上8点搜索AI新闻”。
- 请求 `schedule={type:CRON, cron:"0 0 8 * * ?", timezone:"Asia/Shanghai"}`。
- 返回平台 `scheduledTaskId`、`status=ACTIVE`、`nextExecutionAt=2026-10-27T00:00:00Z`，投递类型 SESSION。这是成功创建证据，不是已执行到时触发的证据，也不能仅因回执没有 expiry 就宣称永久有效。
- runner 0.3.72 二进制内置 `ac_scheduled_tasks`；裸 Claude CLI 未配置该服务器。runner loopback 接口无执行授权返回 403，不复用其他会话凭据或绕过授权。

按最新确认，熟悉提示词优先使用该平台工具及同服务查询工具；未加载或授权失败时使用其他可用定时工具（包括 CronList／CronCreate）查重后创建，明确说明实际有效期与会话限制。此前测试按“无损长期恢复”判定的记录保留；新兜底策略尚未做新的 Agent 实机回归。五字段源 cron 需要按目标实际 schema 转换，不能直接照搬。Skill 已同步到目标用户实际 `.claude/skills/yoooclaw-data-transfer`，测试二进制内嵌同版说明。没有新建或删除现有用户新闻任务。

### 下载进度 HTTP 实测

编译 `internal/transfercloud` Linux 测试二进制，在无影 admin 用户运行，复用生产 `DownloadWithProgress`、OSS SDK 和实际 HTTP 请求；服务端为本机模拟文件服务／OSS／履约，不使用生产凭据。

| 场景 | 实际结果 |
|---|---|
| 慢速下载 19,500,000 字节 | 约 0.001s 报 0%，15.665s 报 43%，22.027s 报 100%；最终文件逐字节一致 |
| 下载途中 Range 断连 | SDK 从中断偏移续传；最终字节一致，进度为 0%、100%，没有累加重传流量 |
| 高频／乱序回调 | 合并最新值、单调增长，不发无变化心跳；完成立即上报 |
| 回报错误 | 临时错误不阻塞下载回调；明确拒绝停止回报；不把错误响应当成功，不接受文件服务的不同响应包装 |
| 安全和配置 | 同源 HTTPS 前缀、明确履约 ID、拒绝越界路径／不同主机／短于 15s 配置；凭据不发给 OSS |

模拟履约收到 `PUT /fulfillment/transfer/<id>/download-progress`，Body 仅 `progress`，响应 `success=true/code=000000/data=null`。云端日志：`/home/admin/transfer-e2e-20261026/download-progress-connected.log`。完整 CLI 二进制同时重新编译安装到独立测试目录，默认 CLI／daemon 未替换。

`go test ./...` 通过；进度相关 race 测试通过。生产履约网关、鉴权、Redis 和目标异步入口仍未联调，因此只能判“进度客户端及云机模拟通过”，不能判“自动迁入端到端通过”。

# 无影异步迁入与状态／进度验收（2026-10-27）

## 环境与结论边界

无影 Ubuntu 24.04 x86_64，admin 用户执行；测试目录 `/home/admin/transfer-e2e-20261027/`。从当前 CLI 工作区交叉编译 Linux CLI 和测试程序，保留独立 `yoooclaw` 可执行文件，未替换默认 CLI／daemon。

当前云机默认 daemon 未配置 `YOOOCLAW_TRANSFER_REPORT_BASE_URL`。本次使用云机上的模拟履约、文件服务和 OSS HTTP 服务，不调用生产凭据。测试实际运行生产 `startTransferImports`、HTTP 路由、持久任务服务、事件／进度客户端、OSS Range 下载器及原导入器。仅将远端 HTTP 目的地址映射到本机模拟服务；入口模拟已认证 Relay 的 loopback 转发，不代表真实 Relay 网络已联调。

全部业务数据和任务记录均在临时目录，使用测试通知验证落盘，不写真实用户 workspace。约 946 MB 可用空间下仅做小样本与约 19.5 MB 慢速下载，没有做 10 GB 压测。

## 异步 HTTP 与事件结果

| 场景 | 云机捕获与验证 |
|---|---|
| 正常迁入 | ACK 200；IMPORT_STARTED → DOWNLOAD_COMPLETED → IMPORT_SUCCEEDED；download-url 仅调用 1 次，测试通知真实落盘 |
| 同请求重复调用 | 200，不重复下载或导入 |
| 同 transferId 换 fileTaskId | 409 |
| 无认证／非法文件 ID | 分别 401／400 |
| 压缩包损坏 | IMPORT_STARTED → IMPORT_FAILED；没有 DOWNLOAD_COMPLETED 或 IMPORT_SUCCEEDED |
| 获取下载链接持续返回 503 | 初次＋3 次重试，共 4 次；约 6 秒后 IMPORT_FAILED |
| 履约拒绝 IMPORT_STARTED（403） | 失败回报；下载链接与 OSS 请求均为 0 |
| 最终结果第一次返回 503 | 约 1 秒后补报同一 IMPORT_SUCCEEDED；下载链接仍只请求 1 次，不重做导入 |
| 迁入超时 | 测试配置 150ms，约 152ms 上报 IMPORT_FAILED；未产生下载完成或成功事件 |

ACK 使用 `success=true/code=000000/data=null`，仅表示持久受理，不表示导入成功。事件真实 HTTP POST 到 `/fulfillment/transfer/events`，下载进度为 PUT 到对应 transferId 的 `download-progress`。事件及进度请求带测试执行端凭据，OSS 请求不带 API Key。

短下载捕获进度 `[0,100]`。损坏包也可能先报下载 100%，随后校验失败：100% 仅代表下载完成，不能替代迁入结果。

## 慢速下载与任务恢复

- 实际下载 19,500,000 字节，收到 0%（1ms）、43%（15.302s）、100%（22.028s），最终文件逐字节一致；中间进度符合至少 15 秒／1% 的限频约定。
- Range 中断后从断点续传，最终 19,500,000 字节一致，未重复累计网络流量。
- 事件客户端正确识别成功、403 永久拒绝及 503 可重试响应。
- 5 个持久任务测试全部通过：重复请求及重启不重做、固定截止、未回报结果不占槽、运行中重启报失败、身份变化不使用新账号回报旧任务。

## 证据与复现

云端日志：

- `async-import.log`：6 个异步场景与捕获的事件、进度及请求次数。
- `download-progress.log`：慢速下载、Range 断线续传及事件响应契约测试。
- `task-recovery.log`：去重、固定截止、重启中断、身份变化和待回报不阻塞执行槽。

运行 `daemon.test` 时设置 `YOOOCLAW_TEST_CLOUD_ACCEPTANCE=1`，选择 `TestTransferCloudAcceptance`；慢速下载设置 `YOOOCLAW_TEST_SLOW_PROGRESS=1`，选择 `TestDownloadProgressSlowCloudProbe`。普通测试默认跳过两项显式验收测试。

本地同一异步验收的 race 测试通过。生产履约部署、真实 Relay 定向通知、App 状态展示及服务端派发前归属／有效期检查，仍需跨服务联调。

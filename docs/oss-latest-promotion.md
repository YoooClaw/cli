# CLI OSS latest 与灰度发布

## 本次范围

测试和生产 CI 均只上传制品并登记草稿。
两区 CI 均不写 `cli/latest`，也不调用推进接口。
灰度平台统一负责版本选择、推进和回退。
生产 latest 在正式版全量后由平台推进。
npm 发布、标签和客户端更新器保持现状。
接受 npm 默认安装提前拿到候选版的边界。

测试构建继续使用 `--test-build`。
测试目标必须配置独立 Bucket 和公共地址。
测试区允许平台选择较低版本的旧分支修复。
这不代表已安装 CLI 会自动降级。

## 对象契约

| 环境 | 公共地址 | 内容 |
|---|---|---|
| 测试 | `https://artifacts-test.yoooclaw.com/cli/latest` | 版本号加换行 |
| 生产 | `https://artifact.yoooclaw.com/cli/latest` | 已全量正式版本号加换行 |

对象 Key 为 `cli/latest`，不是目录。
平台通过对应环境的 OSS SDK 写入。
Content-Type 为 `text/plain; charset=utf-8`。
Cache-Control 为 `no-cache, no-store, max-age=0, must-revalidate`。
写入前检查 `cli/v<version>/` 的制品及校验清单。
写入后从公共入口回读确认。
不重新上传版本制品。
回退采用独立平台操作，不重新触发全量。

## CLI 改动

- 上传器移除 latest 写入能力，包括测试环境。
- 不提供 `--update-latest` 参数或推进接口调用。
- 公共无影脚本不固定本次候选版本。
- 固定版本目录内的无影脚本仍固定版本。
- GitHub 原始安装脚本先读取生产 OSS latest。
- GitHub 只负责下载选定版本，不枚举最高版本。
- 显式指定版本安装继续可用。
- 旧 OSS 回滚工作流停止执行历史发布器。

npm 安装不受上述默认版本规则约束。
自定义 GitHub 仓库应显式指定安装版本。

## 上线顺序及未实现项

1. 平台先接好两区 latest 写入与回读。
2. 保留现有 latest，不能清空默认入口。
3. 再上线本分支的 CI 和公共安装脚本。
4. 两区登记候选后验证默认版本均不改变。
5. 平台全量推进后验证默认安装取新版本。

本次仅修改 CLI，不包含平台服务端实现。
CI 只调用现有草稿登记接口，不发起推进请求。
候选何时成为默认版本由平台独立决定。
操作排序、幂等和回退也由平台负责。
CLI 与平台的双写路径已从本分支移除。

旧 tag 和历史 release 分支仍包含旧上传器。
合入新代码不会改写历史工作流。
切换时须停用历史回滚、补发任务。
两区 CI 身份还应移除 `cli/latest` 写权限。
现有手动 `publish-0.14.1-oss.yml` 使用旧提交，须保持停用。
平台未接好前，不能宣称全量后自动推进已完成。

## 本地验证

上传器测试、安装脚本测试及上传器 vet 通过。
相关工作流通过 actionlint 检查。
Shell 安装脚本通过 bash 语法检查。
测试不连接 OSS，不修改任何线上指针。
本机未安装 PowerShell，Windows 执行验收待 CI。

## 发布上传配置

上传默认使用 OSS_REGION 对应的区域端点。需要全球传输加速时，在仓库 Actions variable
`OSS_ENDPOINT` 配置 `https://oss-accelerate.aliyuncs.com`，三处常规发布入口共同读取该配置；
同名 environment / repository secret 优先，可用于单独覆盖测试或生产环境。
留空或移除两处配置即可恢复区域端点，代码不要求桶必须开启加速。
启用前须确认对应桶已开启传输加速，并验证普通 PUT、分片上传及完成请求；只读探测不能替代写入验证。
若关闭桶的加速，应先清除或改写该配置。显式配置的端点失败会报错，不会自动切换端点。

制品上传默认并发 4，可通过 OSS_UPLOAD_CONCURRENCY 配为 1–8；单文件分片并发保持 3。
仅上传安装器或 dry-run 不读取该并发配置。
任一制品失败会停止派发、取消在途请求，并等待 SDK 清理结束；汇总所有已启动任务的错误后退出，不写 manifest。


### 分片取消与清理

重试期间保留 LeavePartsOnError 与 checkpoint，以便复用已完成分片。
最终失败或取消后，上传器主动 Abort 本次调用已知的未完成 upload ID；清理使用独立 context，
总超时 20 秒，清理失败会在最终错误与日志中明确提示，不输出签名请求。
成功完成的 upload ID 不会被 Abort，也不会清理其他任务或历史分片。
CI 身份需具备相应前缀的 oss:AbortMultipartUpload 权限。

桶仍应配置 AbortMultipartUpload 生命周期规则，兜底 runner 强制退出、初始化响应丢失或清理无权限等情况。
2026-11-12 已按用户要求为生产桶追加 cli-abort-incomplete-multipart-7d：
仅对 cli/ 前缀的未完成分片上传设置 AbortMultipartUpload Days=7，保留原有规则；回读已确认启用。
测试区因当前 PR 分支不满足 environment 部署规则，仍未验证，不应假定测试桶已有生命周期兜底。
context canceled 的并发错误仍保留，用于标明哪些制品受到首个失败影响。


### 手动验证两区加速与 Abort 权限

CI workflow 的 verify_oss_upload=true 输入只运行测试/生产两区隔离探针。
探针使用各环境的 CI 身份，在 cli/runs/oss-upload-smoke/<随机值> 下执行普通 PUT、
文件分片上传和回读校验；另创建一个未完成上传，使用发布器的 cleanup 验证 Abort 权限。
临时对象会删除，不会发布版本、登记草稿或写任何版本指针。
探针也尝试只读查询生命周期规则；没有读取权限时明确标为未确认，不会修改规则。


验证记录（2026-11-12）：
[Actions 43178778892](https://github.com/YoooClaw/cli/actions/runs/43178778892)
生产 job 已通过，确认该环境 CI 身份可经加速端点执行普通 PUT、文件分片上传、回读和 Abort，临时对象已删除。
production environment variable OSS_ENDPOINT 已设为 https://oss-accelerate.aliyuncs.com，合入后发布流程读取。
测试 job 被 environment 部署分支规则拒绝，未接触测试桶；未改保护规则，也未为测试区或仓库配置加速变量。
合入允许的 release 分支后，以 verify_oss_upload=true 补跑，再决定启用测试区加速。


### 生产分片生命周期维护

CI workflow 的 production_lifecycle=inspect 只读备份现有完整 XML 并生成拟追加配置；
production_lifecycle=apply 追加固定的 cli/、7 天 Abort 规则，写入前二次读取以检查并发更新，写入后回读验证。
维护只访问生产桶 yoooclaw-artifacts，拒绝已存在的同名规则或重叠 Abort 规则；
保留全部原始 XML 字段，不修改完整文件过期、历史版本清理等原有设置。
两种模式都会将配置备份保存在 Actions artifact，保留 30 天。

2026-11-12 操作记录：
- [只读检查与备份](https://github.com/YoooClaw/cli/actions/runs/43181209188)
- [追加与回读验证](https://github.com/YoooClaw/cli/actions/runs/43181380186)

生产规则已生效，无需等待代码合并；生命周期执行由 OSS 周期调度，不是即时批量清理。

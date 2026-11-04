# CLI OSS latest 与灰度发布

## 本次范围

生产 CI 上传固定版本制品并登记草稿。
生产 CI 不再写 `cli/latest`。
平台在正式版全量后推进该对象。
npm 发布、标签和客户端更新器保持现状。
接受 npm 默认安装提前拿到候选版的边界。

测试 CI 保留直接推进 latest 的能力。
必须显式传入 `--test-build --update-latest`。
测试目标必须配置独立 Bucket 和公共地址。
测试新发布允许覆盖为较低版本。
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

- 上传器默认不写 latest，包括 `--only artifacts`。
- 仅测试发布允许显式更新 latest。
- 公共无影脚本不固定本次候选版本。
- 固定版本目录内的无影脚本仍固定版本。
- GitHub 原始安装脚本先读取生产 OSS latest。
- GitHub 只负责下载选定版本，不枚举最高版本。
- 显式指定版本安装继续可用。
- 旧 OSS 回滚工作流停止执行历史发布器。

npm 安装不受上述默认版本规则约束。
自定义 GitHub 仓库应显式指定安装版本。

## 上线顺序及未实现项

1. 平台先实现生产 latest 写入与回读。
2. 保留现有 latest，不能清空默认入口。
3. 再上线本分支的 CI 和公共安装脚本。
4. 登记候选后验证默认安装仍取旧全量版本。
5. 平台全量推进后验证默认安装取新版本。

本次仅修改 CLI，不包含平台服务端实现。
本次也不新增未经服务端确认的推进 API。
测试 CI 与平台同时直接写仍存在竞争。
当前行为是最后成功写入的对象生效。
它不能保证旧请求重试不覆盖新决策。
严格操作排序、幂等和统一写入口留待平台对接。

旧 tag 和历史 release 分支仍包含旧上传器。
合入新代码不会改写历史工作流。
切换时须停用历史回滚、补发任务。
生产 CI 身份还应移除 `cli/latest` 写权限。
现有手动 `publish-0.14.1-oss.yml` 使用旧提交，须保持停用。
平台未接好前，不能宣称全量后自动推进已完成。

## 本地验证

上传器测试、安装脚本测试及上传器 vet 通过。
相关工作流通过 actionlint 检查。
Shell 安装脚本通过 bash 语法检查。
测试不连接 OSS，不修改任何线上指针。
本机未安装 PowerShell，Windows 执行验收待 CI。

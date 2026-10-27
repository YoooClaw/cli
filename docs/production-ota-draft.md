# 正式版与 beta 发布登记 OTA 草稿

`cli-v*` tag 发布构建一次：完成签名／公证后，测试区和生产区复用同一批原生二进制，不重新构建、不改成 alpha 版本。

1. 上传测试 OSS，调用测试区 `/api/ota/pipeline/plugin/releases` 创建版本草稿。
2. 测试区登记成功后，继续现有 npm、GitHub Release 和生产 OSS 发布，再调用生产区同一路径创建版本草稿。

正式版 `x.y.z` 和 beta `x.y.z-beta.N` 都执行上述流程。只创建草稿，不配置白名单、灰度比例或调用平台发布接口。现有 npm／OSS 发布行为保留；“仅草稿”指灰度平台状态。

- GitHub `test`、`production` Environment 分别配置 `OTA_PLUGIN_RELEASE_PIPELINE_TOKEN`，仅通过 `X-CI-Token` 请求头传递。测试 OSS 复用该环境已有 OSS Secrets。
- 每区登记五个平台的版本固定二进制 URL，指向各自 OSS，不使用 latest 地址。测试区使用同一版本号；beta 不覆盖稳定版 latest。
- 测试上传与日常 alpha 测试部署共用并发组。日常 release 分支推送的 alpha 测试流程保持不变。
- 创建请求失败会使流水线失败，不自动重试；超时可能已创建成功，重跑前先核对平台，避免重复草稿。测试登记失败时不会继续生产发布。
- 已发布旧版本不会自动补登记；改动合入 release 后，后续新 tag 才使用此流程。

本地验证登记请求、版本和平台映射，不向测试或生产平台实际创建版本。

## 0.14.0 补登记记录（2026-10-27）

- 原 `cli-v0.14.0` 发布标签未包含生产草稿登记步骤，npm 和生产 OSS 发布已成功。
- 已通过流水线草稿接口补登记：测试区 releaseId=54，生产区 releaseId=7；两次响应均为 `success=true`、`code=000000`。
- 两区均引用 `https://artifact.yoooclaw.com/cli/v0.14.0/` 下现有五平台制品；各 URL 已确认 HTTP 200。此次不重新构建、不修改 latest、不配置阶段、不启动发布。
- 后续 beta／正式版按照本文双环境流程执行，各自上传 OSS 并登记草稿。该流程已在 `release/0.15.0`，本次同步至 0.14.0 维护线。

# 生产流水线登记 OTA 草稿

生产发布流程在 OSS 上传成功后，调用生产区 `/api/ota/pipeline/plugin/releases` 创建版本草稿。流水线只登记，不配置白名单、灰度比例或执行平台发布。

- GitHub `production` Environment Secret：`OTA_PLUGIN_RELEASE_PIPELINE_TOKEN`，仅通过 `X-CI-Token` 请求头使用。
- 登记脚本支持 `x.y.z-alpha.N`、`x.y.z-beta.N`、`x.y.z`；环境由工作流的接口地址决定，格式支持不改变现有发布触发规则。
- CLI 登记五个平台的原生二进制 URL；插件登记版本固定 tgz URL。均指向生产 OSS，不使用 latest 地址。
- 创建请求失败时流水线失败，不自动重试；超时可能已创建成功，重跑前先核对平台，避免重复草稿。
- 已发布的旧版本不会因配置 Secret 自动补登记；本改动进入 release 分支并随后发布新版本时生效。

验证覆盖三种版本格式、非法版本、Token 仅入请求头、业务失败不重试，以及生产工作流在上传完成后登记草稿。本地测试未向生产接口创建测试草稿。

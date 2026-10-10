# 发布与安装

本阶段的用户确认要求来自产品交付流程：用户先试玩并认可当前候选版本，再提供最终安装包。准备和测试均在确认前完成；确认后直接交付，不再重复询问。

在对话中确认用户认可的是当前报告对应版本后执行：

```bash
python3 "$SKILL/scripts/pipeline.py" release --project "$PROJECT" --name "$NAME" --approved
```

`--approved` 是 Agent 对既有用户认可的记录，不会替代实际认可。脚本验证测试报告、包文件、源码、场景、SDK 预览文件均未变化；失败则回到构建/验收。

交付 `release/<name>.zip` 和 `.sha256`，并说明：

1. 硬件需集成与 SDK 快照兼容的 NanoShell，预览通过尚不等于真机验收。
2. 解压得到 `<name>.nsp/`，其中包括 `manifest.json`、`app.wasm`、`README-INSTALL.txt`。
3. 整个目录复制到设备 `/SD:/ns_data/packages/<name>.nsp/`。
4. 在上位机执行“安装 packages”，再进入 NanoShell 大厅启动应用。

ZIP 是传输容器，当前固件安装对象是 `.nsp` 目录；不要指导用户直接向设备导入 ZIP，除非对应上位机明确支持解压安装。BLE 推包可作为设备已支持时的替代路径，具体连接参数由设备环境提供。

## 注册到 CLI，供 App 查询和下载

最终安装包必须通过 CLI 发布命令保存，不能仅留在临时工作目录，也不要手工写索引或移动文件进存储目录。源码、预览和 QA 留在项目中；正式包由 CLI 管理。

```bash
yoooclaw --profile "$PROFILE" nanoshell storage-path
yoooclaw --profile "$PROFILE" nanoshell publish --package "$PROJECT/release/$NAME.zip" --client "$CLIENT_LABEL"
```

存储位置默认是 `~/.yoooclaw/profiles/<profile>/nanoshell/`，与 `notifications/`、`recordings/` 平级；遵循 `YOOOCLAW_HOME` 与 profile 配置，不写死 home 路径。目标客户端必须是已有 API-key 的 label，沿用当前会话确定的客户端；无法唯一确定时先询问，不猜测归属、不广播发布。缺少支持该命令的 CLI 时说明尚未完成 App 发布，不伪称 App 已可下载。

发布成功后记录 `appId`、`version`、`packageId`；App 使用 `nanoshell.apps.list` 和 `nanoshell.apps.download` 获取该包。包哈希即 packageId，同一包重复发布幂等；同一应用同一版本的不同包会被拒绝。更新时保持稳定 `--id`，在 `pipeline.py build` 指定递增的 `--version`，再重新验收和试玩。

发布 ZIP 只含一个 `.nsp` 目录，使用固定时间戳、权限和文件顺序；验收报告留在 `qa/`，不嵌入 ZIP。下载成功与硬件安装成功是两个状态。CLI 必须在线，且 App 连接到相同目标 CLI 和账号。

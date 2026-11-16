# 上下文更新事件

CLI 接收并提交上下文后，记录 `context.updated`。消费者收到事件后通过现有读取接口获取最新数据。事件默认记录，不需要启用开关；不会自动运行 Agent 或 shell。

## 订阅与回放

以下示例中的地址与 token 使用当前 profile 的 daemon 配置。

```sh
# 查询保留窗口内的事件；后续将 nextCursor 传给 after。
curl -H "Authorization: Bearer $YC_TOKEN" \
  'http://127.0.0.1:18789/context/events?limit=100'

# 从连接时刻开始订阅。续接时加 -H "Last-Event-ID: <cursor>"。
curl -N -H "Authorization: Bearer $YC_TOKEN" \
  'http://127.0.0.1:18789/context/events/stream'
```

查询返回 `events`、`nextCursor`、`hasMore`、`highWatermark`。`limit` 为 1–500，默认 100。没有 `after` 时查询保留历史；SSE 没有游标时只看新事件。带游标时两个接口均从该位置之后回放。`after` 与 `Last-Event-ID` 同时提供时必须一致。

SSE 帧示例：

```text
id: <opaque-cursor>
event: context.updated
data: {"schemaVersion":1,"eventId":"<uuid>","cursor":"<opaque-cursor>","occurredAt":"2026-11-16T00:00:00Z","source":"data","operation":"append","scope":{"kind":"client","clientLabel":"phone"},"result":{"committedCount":1,"partial":false,"unit":"objects"},"resourceIds":["<object-id>"]}
```

消费者按 `eventId` 去重，处理成功后保存游标。查询即使没有可见事件也要保存 `nextCursor`，这样不会反复扫描其他客户端的事件。SSE 每 15 秒发心跳；最多 32 个连接，写入超时 10 秒。API key 撤销后在下一次鉴权检查断开，空闲时最长约 1 秒。

HTTP API key 只能读取自己的私有事件及共享事件；gateway token/本机管理身份可以读取整个 profile。Relay 请求桥接使用查询接口轮询，不能承载 SSE；对 Relay 的 SSE 请求返回 `STREAM_REQUIRES_HTTP`。事件内容不含正文、位置、健康值、下载地址或文件绝对路径。

## 来源与提交单位

| source | operation | unit | 含义 |
| --- | --- | --- | --- |
| notifications | append | records | 实际新增通知数 |
| locations | append | records | 完整追加的位置记录数 |
| calendars | replace | days | 实际改写的日期文件数，包含清空 |
| reminders | replace | snapshots | 内容变化的已应用快照，计数 1 |
| health-data | replace | days | 实际改写的健康日期文件数 |
| images | upsert | objects | 图片元数据提交一次；后台文件下载成功且索引更新后再通知一次 |
| web-pages | upsert | objects | 索引可达的网页内容变化 |
| recordings | upsert | objects | 索引可达的转写/摘要变化；音频下载进度不触发 |
| data | append | objects | 新增可读取对象，包括直接写入、分片 commit、OSS 导入 |

`partial=true` 表示该批次之后还有步骤失败，但事件所指部分已经可读；即使接收请求最终返回错误也可能产生这种事件。空批次、校验失败、拒绝的旧快照、没有内容变化的重复提交不会产生更新。位置存储自身不去重，重复追加仍是新增记录。

通用 data 的 blob 去重不等于对象去重：同内容新增对象 ID 仍发事件；幂等重试返回已有对象不发。上传 begin/chunk/status/abort 和 OSS 下载进度不触发，`data.import.status` 继续保留。全量工作区迁移、删除及 GC 不在该事件范围。

事件 `scope` 遵循已有来源数据的可见性。位置、日程、提醒、健康数据为共享来源；有 clientLabel 的来源保留私有归属，历史共享标签保持共享。共享事件不会经 Egress 广播到所有手机。

## 实时回调与恢复

私有事件持久化后，同时尝试现有 Egress：standalone 走指定客户端的 Relay；proxied POST `{event,payload,clientLabel}` 到既有宿主回调；direct 只使用本地查询/SSE。实时投递最多 8 个并发任务，忙时跳过快捷投递，日志仍可回放。HTTP 回调沿用原有有限重试；事件出现不代表宿主已经处理完成。

日志在 profile 的 `state/context-events/`，私有权限、约 1 MiB 分段，最多保留 7 天且总分段大小不超过 64 MiB。按整段清理，因此不能依赖任何事件一定保留满 7 天。损坏的尾部未完成记录可截断恢复；中段损坏会记录错误并重置日志代次。

游标过期或代次不符返回 `410 CURSOR_EXPIRED` 和新 `highWatermark`。消费者先保存该水位，再重新读取当前上下文，随后从该水位补读。SSE 已建立后遇到过期，发送 `event: error`，携带同样的错误码及水位，然后关闭。非法游标返回 400，日志不可用返回 503。

数据与事件日志不是原子事务：进程在数据写入后、事件记录前崩溃可能漏事件。日志写入失败会记录 `context.event.persist_failed`，不会回滚已提交的数据，也不会改变原有成功响应。若启动时日志初始化失败，daemon 仍可接收数据，事件接口返回 503；修复存储后需重启以重新初始化日志。消费者应首次启动和定期重新核对当前数据，不能把通知日志作为业务数据的唯一副本。

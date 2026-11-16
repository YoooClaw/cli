# 0.18.0 上下文接收完成事件方案

状态：已实现，2026-11-16 本地验证；接口使用见 [context-events.md](../context-events.md)。
基线：master `6f4e948`；工作分支：`release/0.18.0`。

## 1. 目标与语义

手机或宿主下发上下文，CLI 完成可读取的数据写入后，产生统一的 `context.updated` 事件，供本地服务、Agent 宿主订阅并决定后续动作。这里的 hook 是结构化事件订阅接口，不执行任意 shell，也不自动启动 Agent。

事件表示“某来源的数据已成功应用，请重新读取”，不表示模型已经收到 prompt、消费者已经处理或整批业务全部成功。默认启用事件记录，不增加功能开关。

## 2. 当前代码与缺口

- `internal/daemon/server.go`：通知写入后，`recordIngest` 只更新计数与最后接收时间。
- `server_location.go`、`server_calendar.go`、`server_reminders.go`、`server_healthdata.go`：写入后记录日志并返回响应，没有统一事件。
- `server_ingest.go`、`server_webpage.go`：图片、网页接收同样缺少统一事件；录音已有独立的 `recording.status`。
- `server_data.go`、`server_data_records.go`、`server_data_import.go`：通用对象写入包括直接上传、分片提交和 OSS 导入；目前没有统一上下文事件。
- `internal/daemon/egress.go`：standalone 走 Relay，proxied 回调宿主，direct 使用 `NoopEgress`。因此只调用 `PushEventTo` 无法满足本地订阅。
- `internal/recording/events.go`：已有 JSONL 状态日志，但没有统一游标、订阅和恢复协议，不能直接当成可靠事件队列使用。

## 3. 范围与触发条件

以一次成功提交的接收批次为单位发事件，避免逐条通知唤醒消费者。校验失败、鉴权失败、完全去重、拒绝过期快照不发事件。空快照若确实清空旧数据，应发事件。

| 来源 `source` | 接入位置 | 触发条件 |
| --- | --- | --- |
| `notifications` | `/notifications`、`/gateway/notifications.push` 的共同写入路径 | 实际成功新增数大于零；部分失败时报告成功数及 `partial=true` |
| `locations` | `/locations` | 成功追加非空批次 |
| `calendars` | `/calendars` | `Applied=true`；包括生效的空快照 |
| `reminders` | `/reminders` | `Applied=true`；包括生效的空快照 |
| `health-data` | `/health-data` | 至少一个指标、睡眠会话或删除标记实际应用 |
| `images` | `/images`、`/gateway/images.sync` | 新文件或元数据实际更新成功 |
| `web-pages` | `/web-pages` | 正文或可读取版本实际更新成功 |
| `recordings` | 录音结果写入及后台转写的共同提交位置 | 正文或摘要可读内容提交成功；下载进度变化不发本事件 |

| `data` | `POST /data`、`/gateway/data.write`、`data.upload.commit`、`/data/import` 与 `/gateway/data.import` 的最终对象提交路径 | 新对象记录及其内容成功提交并可读取；幂等重试返回已有对象不重复发 |

通用 `/data` 写入属于本期范围，包括直接上传、分片上传最终提交、OSS 单对象导入完成。全量工作区迁移导入、读取查询、memory 派生生成、录音重命名/删除等仍不属于本期范围，不从目录监听反推事件。

### 通用 data 写入的具体约定

- 统一使用 `source=data`、`operation=append`、`committedCount=1`，`resourceIds` 包含已提交对象 ID。对象自带的 `Metadata.Source` 不覆盖事件来源分类；消费者按现有 data API 读取对象。
- `Store.Put` 与 `Store.ImportFile` 共享底层 `put`；实现时在共同提交结果中显式区分“新建对象记录”和“返回已有对象”。当前 `PutResult.Deduped` 同时可能表示 blob 内容复用或幂等命中，不能直接用它决定是否派发：复用 blob 但新增对象 ID 仍然发事件；幂等命中已有对象不发。
- 分片上传 begin/chunk/status/abort 不发；只有 commit 产生新的可读对象时发。重复 commit 返回已有回执不重复发。
- OSS 导入受理返回 202、下载进度、失败和状态查询不发上下文事件；最终对象提交成功时发。保留现有 `data.import.status` 作为任务进度事件，两者语义独立。导入重试或重启恢复发现已有对象时，不当作新对象再次派发；数据提交与事件记录之间的崩溃窗口仍遵循第 7 节限制。
- 事件可见范围取已提交对象的真实 `ClientLabel`，包括存储对空 label 归一化为 `local` 的行为；不能使用用户提供的 metadata 或 source 推导身份。
- 通用 data 的删除、GC 与 retention 清理不触发本期“写入完成”事件。


实现前需给尚未返回应用结果的 storage 补充 `Changed/Applied/CommittedCount` 等提交信息。不得把收到请求或输入条数当成成功写入数。多文件操作如可能部分提交，必须返回已提交部分的结果；不能仅凭最终 error 判定完全没有变化。

## 4. 架构

```text
HTTP / Relay / 宿主转发
        ↓ 原有鉴权、校验、去重、存储
成功提交结果（含来源与可见范围）
        ↓
统一 ContextEventPublisher
        ├─ 本地有界事件日志 → 游标查询 / SSE 订阅
        └─ 有确定接收方时 → 现有 Egress → Relay / 宿主回调
```

建议新增 `internal/contextevent`，负责事件结构、串行追加、查询、保留策略和订阅通知；daemon 负责鉴权及组装事件。业务 storage 返回提交事实，不直接依赖网络传输。

推荐把发布调用放在共同的业务提交路径，避免 HTTP 和 gateway 别名漏接或重复发。录音异步任务携带原始来源身份，不依赖已结束的 HTTP 请求。发布时先记录本地日志，再通知订阅者和 Egress；网络工作不阻塞 ingest。

## 5. 事件协议 v1

```json
{
  "event": "context.updated",
  "payload": {
    "schemaVersion": 1,
    "eventId": "<uuid>",
    "cursor": "<opaque-stream-position>",
    "occurredAt": "2026-11-15T08:00:00Z",
    "source": "notifications",
    "operation": "append",
    "scope": { "kind": "client", "clientLabel": "<label>" },
    "result": { "committedCount": 12, "partial": false, "unit": "records" }
  }
}
```

- `operation`：`append`、`replace`、`upsert`，与实际存储语义对应。
- `eventId` 用于幂等；同一事件重发不换 ID。不同接收请求如实际再次提交，可以产生新事件。
- `cursor` 为不透明的日志代次与序号；分配顺序是发布顺序，不承诺跨来源数据的业务先后顺序。
- `occurredAt` 是 CLI 提交后的事件创建时间；来源采集时间如存在，另放可选 `collectedAt`。
- `requestId` 可选；只从可信请求上下文获取。
- `scope.kind` 为 `client` 或 `shared`，依据现有数据可见性生成，不能直接信任请求体里的标签。
- 可选 `resourceIds` 限制最多 100 个，超限带 `resourcesTruncated=true`；数据量大时消费者按来源重读。
- 不含通知正文、健康指标、位置坐标、转写正文、密钥和绝对文件路径。

事件是重新读取提示，不是完整数据副本；重复事件可按 `eventId` 去重，密集事件可按来源合并读取。

## 6. 消费接口与身份边界

新增同一 daemon 下的只读接口，沿用现有鉴权；具体接线必须覆盖 daemon 的路由和鉴权白名单判断：

- `GET /context/events?after=<cursor>&limit=100`：返回 `events`、`nextCursor`、`hasMore`、`highWatermark`；limit 最大 500。无 after 默认从保留窗口起点读取。
- `GET /context/events/stream`：SSE，事件名 `context.updated`，`id` 为 cursor，`data` 为 payload；支持 `Last-Event-ID`，也支持 after 参数，二者同时提供且不同时返回 400。
- SSE 无游标默认从连接建立时的水位开始；需要历史时先查询或显式给游标。每 15 秒发送心跳，断开及时释放资源。
- 回放与实时订阅必须无缝衔接：订阅者收到的内存通知仅用于唤醒，实际始终按日志游标读取，避免注册窗口丢事件。
- 游标过期或日志代次不符返回 `410 CURSOR_EXPIRED`，提供新的水位；消费者先获取该水位，再重新读取当前上下文，然后从该水位补读事件。
- 客户端身份只读自身及现有规则允许的共享事件；profile 管理身份可读该 profile 的事件。过滤后返回的扫描游标也必须前进，避免无权限记录导致死循环；游标不作为访问凭证。
- 长连接定期复核凭证，凭证撤销或权限变化立即终止；不能只在连接建立时鉴权。

Egress 只是可选的实时快捷通道。`client` 事件使用 `PushEventTo`；`shared` 事件不自动广播给所有手机，保留在有权限的本地订阅中。现有部分来源没有存储级 clientLabel 隔离，本期不得将请求标签误当成数据所有权，也不借本功能偷偷修改原有数据可见性。

宿主收到新事件后是否启动 Agent，属于宿主的消费策略。CLI 发布事件完成不等于 OpenClaw/Thread 已完成订阅；跨仓库适配需要独立验收。

## 7. 可靠性与资源边界

本期采用“有持久日志的可恢复通知”，不承诺数据与事件原子提交或 exactly-once。

- 日志位于 profile 状态目录 `context-events/`，权限沿用私有状态文件规范；串行写入，单条记录有大小限制。
- 分段 JSONL 日志，默认保留 7 天且总量不超过 64 MiB，任一上限触发清理；游标维护独立持久代次/序号，日志清理不复用旧游标。
- 启动恢复只容忍尾部未完成记录；中段损坏不得静默跳过，记录错误并切换日志代次，让客户端明确重新同步。
- 本地日志写入失败时，保留已经成功写入的数据及原有 ingest 成功响应，输出 `context.event.persist_failed` 结构化错误；不通过 Egress 单独发送未记录事件。
- 数据提交后、事件记录前进程崩溃仍可能漏事件。消费者首次连接、游标失效以及周期性对账时应重新读取当前数据。若要求此窗口也不能漏，需单独引入与数据写入一致的事务 outbox/提交日志，不能声称现有多文件存储已具备此保证。
- 日志持久化后的事件允许重复回放；消费者只在处理成功后持久化自己的游标。CLI 不保存消费者 ack。
- 每个 SSE 连接使用有界唤醒队列；慢消费者落后超过保留窗口时断开并要求重新同步，不无限缓存。
- Egress 继续沿用既有重试语义；回调失败不会阻止本地日志回放。日志分别记录“已持久化”“已尝试投递”“投递失败”，不把调用返回等同于消费者已处理。

## 8. 实现步骤

1. 核定各来源提交结果与部分写入语义，补充必要的 storage 返回信息。
2. 实现 `contextevent` 协议、日志、游标、恢复与保留清理。
3. 在九类来源的共同提交位置接入发布器，保留已有业务响应和录音状态事件。
4. 实现鉴权后的查询与 SSE；补充事件 scope 映射和凭证撤销处理。
5. 接入定向 Egress，补充消费者示例及 API 文档。
6. 本地端到端验收；如需要宿主实际触发，再安排对应仓库适配及联调。

## 9. 验收标准

- 九类来源分别验证：成功提交后可读事件、事件到达时数据已可读取。
- 同一通知走 HTTP/gateway 两种入口行为一致；重复通知、过期快照不发；空快照清空会发。
- 通用 data 验证直接上传、gateway 写入、分片 commit、OSS 导入四条路径；提交时对象可读且 ID 对应，进度不误发，重复 commit/幂等重试不重复发，blob 去重但新对象仍发。
- 部分成功只报告已提交部分；失败和计数不能伪装为整批成功。
- 录音进度仍走 `recording.status`，正文/摘要提交才发 `context.updated`，无重复接线。
- direct 模式能订阅；standalone/proxied 保留已有行为并验证新事件定向传输。
- 验证断线续读、daemon 重启回放、SSE 衔接并发窗口、慢消费者、保留窗口淘汰、日志尾部损坏及磁盘写失败。
- 两个 clientLabel 不能互读私有事件；未知来源不广播；共享来源按现有可见性处理；撤销凭证后长连接失效。
- 并发测试使用 race 检查；端到端用真实 daemon 接收合成数据，订阅收到事件后再读取对应上下文。

实现未修改版本号，也未执行正式发布。当前实现使用 `result.unit` 区分 records/days/snapshots/objects；日程和健康计数为实际改写日期数，提醒事项只在快照内容变化时通知。图片元数据提交和后台下载完成分别通知，消费者按事件重新读取当前状态。

SSE 仅支持直接 HTTP，Relay 请求桥接通过游标查询恢复；连接上限 32，凭证每秒复核。实时 Egress 投递使用 8 个并发槽，繁忙时保留日志并跳过快捷投递。分段清理以段内最早记录时间为准，保留时间为上限。v1 暂不填充可选 requestId/collectedAt；异步处理不复用失效请求上下文。

本地验证、宿主联调和正式发布分别报告结果。

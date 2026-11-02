# 资产每日汇总接口 V1

`GET /assets/summary` 一次返回七类资产的每日数量及最新展示记录。
健康只返回最新一个有效业务日的快照，不统计或返回 `count`。
接口复用 daemon 鉴权、只读 messageBridge 信封、完成日志和 Relay 转发。
能力标记为 `assets.summary.read.v1`，由 `/health` 与 gateway health 同时返回。

## 请求

```text
GET /assets/summary
GET /assets/summary?date=2026-10-30
GET /assets/summary?date=2026-10-30&types=notifications,health-data
```

| 参数 | 规则 |
| --- | --- |
| `date` | 可选 `YYYY-MM-DD`，默认 CLI 主机本地今天 |
| `types` | 可选逗号分隔类别；默认全部七类，仅查询选中类别 |

类别为 `notifications`、`locations`、`calendars`、`reminders`、`web-pages`、
`recordings`、`health-data`。空值、重复类别、重复参数、未知参数/类别及非法日期
均返回 400 `INVALID_PARAMS`。不接受分页、profile、路径或任意时区参数。
本次未新增图片、口述历史或桌面采集器的汇总能力。

客户端沿用现有 `POST /api/message/messageBridge/send`：

```json
{
  "path": "/assets/summary?date=2026-10-30&types=notifications%2Chealth-data",
  "payload": "{\"method\":\"GET\",\"body\":null}"
}
```

外层鉴权及 `X-Api-Key-Id` 选路保持原方式。CLI 路由同时接受
`POST /assets/summary?...`，正文为上述 GET 信封；校验及 4096 字节上限
与现有资产列表共用。不接受写操作。

## 统计与最新记录口径

| 类别 | 当天数量范围 | 最新一条 | 读取方式 |
| --- | --- | --- | --- |
| 通知 | 目标本地日期文件内的有效通知，去重后实际存量，全部来源 | `timestamp` 最大，同刻取后行 | 复用 `.stats` 摘要，失效时单日重建 |
| 位置 | 目标接收日期文件内有效记录，不做停留聚合 | 有效 `timestamp` 最大；全无时间则取后行 | 单个日期文件，捕获长度后读取 |
| 日程 | 目标日文件内的展示项，包括覆盖当天的跨天事件 | `startAt` 最大，同刻取后行 | 单个日期文件，不跨日填页 |
| 提醒事项 | `due.date` 等于目标日，含已完成与未完成，无截止日期项排除 | 当天到期墙上时间最大；仅日期排在带时间之前；同值按 recordId 降序 | 当前镜像精简索引，不读取备注和详情 |
| 网页剪藏 | 当前 URL 索引项的 `capturedAt` 换算主机本地日期后等于目标日 | `capturedAt` 最大，同刻按 urlHash 降序 | URL 索引，不读取网页正文/版本正文 |
| 录音 | 当前可见索引中 `metadata.created_at` 换算本地日期后等于目标日 | `created_at` 最大，同刻按录音 ID 降序 | 持写锁读取已提交元数据，不读取音频或转写 |
| 健康 | 不统计数量；不受请求 `date` 限制 | 最新业务日期的有效 v3 快照 | 日期倒序查找，返回日指标和睡眠摘要，不带阶段详情 |

请求的默认“今天”来自主机时区；原始存储的日期归属不变：通知按通知时间、
位置按 CLI 接收日；日程按原事件时区分配的日期；提醒按原 `due.date`。
不会为了本接口重新归档数据或把所有日期文件强制换算成同一时区。
网页统计当前索引项，不是当天每一次重复剪藏的次数；日程和提醒也不是当天新增/上传数。
日程最新按开始时间、提醒最新按到期时间，不代表最新编辑/创建或下一条待办。

通知等六类新资产视图读取当前单用户 profile 的全部来源；录音继续保留旧录音列表的
clientLabel 可见性（当前来源与共享历史），不会借新接口扩大原有录音读取范围。

## 响应

```json
{
  "ok": true,
  "partial": false,
  "date": "2026-10-30",
  "timeZone": "Asia/Shanghai",
  "dayStart": "2026-10-30T00:00:00+08:00",
  "dayEnd": "2026-10-31T00:00:00+08:00",
  "generatedAt": "2026-10-30T12:00:00+08:00",
  "assets": {
    "notifications": {
      "status": "ok",
      "count": 200,
      "latestBy": "timestamp",
      "latest": {
        "recordId": "opaque-summary-record-id",
        "storageDate": "2026-10-30",
        "status": "ok",
        "data": {"appName": "example", "title": "会议提醒", "content": "下午开会", "timestamp": "2026-10-30T03:00:00Z"},
        "truncatedFields": []
      },
      "warnings": []
    },
    "health-data": {
      "status": "ok",
      "latestBy": "date",
      "latest": null,
      "warnings": []
    }
  }
}
```

示例为选择通知和健康、暂无 v3 健康数据的情况；通知 data 的可空展示字段为简洁省略。
健康有数据时 `latest` 的 data 与 `/assets/health-data` 日记录一致，包含 revision、
日指标和睡眠摘要，始终没有外层 `count`。无数据时其他类别返回 `count:0, latest:null`。
`timeZone` 可能是系统名称 `Local`；需要准确本地边界时使用带偏移的
`dayStart` 和排他的 `dayEnd`，夏令时日不一定为 24 小时。

每个类别独立返回 `status`、`latestBy`、`latest`、`warnings`，成功计数类型还有 `count`。
`latest` 沿用资产 Item 信封和展示字段投影，网页只返回 title/url；
录音只返回 recordingId/name/created_at/时长/状态/产物可用布尔值，不返回本机文件路径。
通知/位置/日程的汇总 recordId 是摘要标识，不保证与逐条列表中的行 ID 相同；
提醒沿用原详情 recordId，健康沿用原业务日期 ID，均不产生分页 cursor。

## 错误与读取预算

- 鉴权失败为 HTTP 401，错误结构与现有资产接口一致；参数错误为 400，写方法为 405。
- 某类别读取失败时仍返回 HTTP 200、`ok:true, partial:true`；该类为
  `status:error, latest:null, error:{code,message,retryable,action}`，省略 `count`，不能显示成 0。
  其他类别照常返回；所有类别失败也按此规则逐类报告。
- 源文件损坏/不可读为 `SOURCE_READ_FAILED`，变化冲突为 `SOURCE_CHANGED`，
  超出扫描预算为 `SOURCE_TOO_LARGE`；只返回通用错误，不暴露本机路径/正文。
- 通知冷重建最多 32 MiB，已有有效摘要可直接读取更大的日期文件；索引自身最多读取 32 MiB。
  普通写入继续沿用原存储规则，不受 HTTP 冷重建预算约束。
- 位置/日程单日最多读取 4 MiB；网页/录音索引最多 32 MiB。
  提醒沿用现有镜像索引与恢复上限；健康最多检查 14 个日期文件、合计 4 MiB，跳过旧格式时带警告。
  预算不足不会把已扫描的部分计数当完整数量，也不会把尚未查完的健康当作无数据。
- 位置/日程 JSONL 坏行跳过并返回累计警告；通知索引沿用有效记录统计规则，
  不额外返回历史坏行数量。网页和录音的无效时间不能归入指定日期，跳过并返回警告。
- 每类最新展示项最多 32 KiB，总响应最多 256 KiB。长展示文本裁剪并标记
  `truncatedFields`；无法缩小的健康快照/URL/ID 返回 `status:oversized, data:null` 占位，
  已成功算出的数量仍有效。完整列表及详情继续走原接口。

各类并发读取，分别使用自己的存储锁和快照，不是跨存储的全局事务快照。
通知源数据不改写，按需重建时可更新派生 `.stats` 索引；提醒读取沿用既有中断事务恢复机制。
HTTP 返回 `application/json; charset=utf-8` 和 `Cache-Control:no-store`。
日志记录日期、类别数、partial、响应大小和耗时，不记录最新通知正文等资产内容。

## 实现与验证

- `internal/assets/summary*.go`：请求/响应与并发编排、单日读取、各类索引读取分文件。
- `internal/daemon/server_assets_summary.go`：路由响应与完成日志；鉴权和只读信封复用既有入口。
- `internal/notif`：复用每日摘要，新增可取消、带冷重建预算的读取入口。
- `internal/recording/read_snapshot.go`：只读已提交索引并显式返回损坏/超大等错误。

测试覆盖七类统计/最新/健康无 count、默认/指定日期、按需类别、空数据、UTC/本地日及夏令时、
高精度数字、去重与迟到通知、部分失败、读取/响应预算、原录音可见性、鉴权、
GET/POST 信封一致、实际 loopback HTTP/完成日志、模拟 Relay 两种方法及并发上传查询。
所有数据与网络验证使用临时目录和本地测试服务，不修改运行中 CLI 的用户数据。

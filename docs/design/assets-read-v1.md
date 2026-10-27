# 资产读取接口 V1

本实现新增五类只读 HTTP 接口，复用 daemon 的鉴权、日志及 Relay HTTP 转发。
录音继续使用原来的 `POST /gateway/recordings.list`，口述历史没有新增接口。
客户端负责分组与展示；读取接口不聚合通知、不计算停留时长、不汇总健康指标。

早期设计稿及请求/响应示例见 [Thread 资产读取接口设计 V1](https://chatgpt.com/space/page_b39395087e088191a0480b6d5b64ab73)。
当前字段与分页契约以本文为准。2026-10-22 已同步单用户 profile 和全部来源读取规则；本机 CLI 已安装五类读取接口，
iOS 最小列表页已完成源码接入与隔离测试，尚未构建或发布客户端，也未部署到其他机器。

## 路由和请求参数

| GET 路由 | 默认 limit | 最大 limit | 分页单位 |
| --- | ---: | ---: | --- |
| /assets/notifications | 50 | 100 | 原始通知 |
| /assets/locations | 50 | 100 | 原始位置记录 |
| /assets/calendars | 50 | 100 | 当天的一次日程展示 |
| /assets/health-data | 7 | 14 | 健康日指标及睡眠摘要 |
| /assets/web-pages | 20 | 50 | 当前 URL 索引项 |

所有接口接受可选的 `limit`、`cursor`。limit 必须为十进制正整数，
超过最大值返回 400，不自动截断。cursor 最长 4096 个 ASCII 字符，空串等同省略。
未知参数和重复参数均返回 400；不接受 profile、文件路径、日期范围或 clientLabel 参数。
同一个 cursor 可以更改 limit。

### 客户端 messageBridge 兼容

现有 `POST /api/message/messageBridge/send` 仅向 CLI 转发 POST，因此五条资产路由
及睡眠详情路由同时接受只读信封 `{"method":"GET","body":null}`。外层请求为
`{"path":"/assets/notifications?limit=50","payload":"{\"method\":\"GET\",\"body\":null}"}`，
继续使用当前登录鉴权与 `X-Api-Key-Id` 选路。所有分页参数放在 path 的 query 中，
不放进 payload；cursor 必须 URL 编码。信封最大 4096 字节，method 必须为 GET，
body 只能省略或为 null，额外字段、写方法及多段 JSON 均返回 400。
GET 与 POST 信封共享鉴权、返回结构、分页上限和 cursor；这不是资产写入接口。
iOS 默认每次读取 50 条通知/位置/日程、7 天健康数据或 20 篇网页。

日程首次请求额外接受：

- `direction=forward|backward`，默认 forward。
- `timeZone`：IANA 时区；默认使用可识别的本地 IANA 时区，否则 UTC。
- `anchorDate=YYYY-MM-DD`：默认是上述时区的今天。

forward 读取不早于 anchorDate 的日期文件，日期升序、文件内正序；
backward 读取不晚于 anchorDate 的日期文件，日期降序、文件内倒序。
后续请求可以省略这三个参数，重复提供时必须与 cursor 中的已解析值一致。
时区只影响默认起点，不重分组已保存的日程。

## 返回结构

```json
{
  "ok": true,
  "items": [
    {
      "recordId": "opaque-record-id",
      "storageDate": "2026-10-22",
      "status": "ok",
      "data": {},
      "truncatedFields": []
    }
  ],
  "nextCursor": null,
  "warnings": []
}
```

日期类的 storageDate 是文件日期；网页为 null。所有约定可空字段显式返回 null。
recordId 标识原始行或展示项；同一文件正常追加不改变既有项 ID，
即使追加前最后一行没有换行。JSONL 的身份位置采用记录末端字节，排除 LF 分隔符。
日程同一事件跨天有不同 recordId；健康按 profile 和业务日期保持 ID，
用 data.revision 标识内容更新；网页按 profile 和完整 URL hash 保持 ID。

| 类别 | data 字段 |
| --- | --- |
| 通知 | appName、appDisplayName、title、content、timestamp、senderName、conversationType、conversationName、clientLabel |
| 位置 | id、timestamp、longitude、latitude、accuracyMeters、coordinateSystem、poi、address、provider、deviceType |
| 日程 | id、calendarName、title、startAt、endAt、timeZone、isAllDay、location、description、reminderTimes |
| 健康 | schemaVersion=3、date、timeZone、provider、updatedAt、revision、items、sleepSessions |
| 网页 | title、url |

位置缺失或类型错误的可选字段返回 null，经纬度 0 有效，不转换坐标系。
健康 items 保留存储的 metric/name/value/unit，不补零，不合并指标，不拆分整日。
睡眠独立放在 sleepSessions 摘要中，列表不含 segments；v1/v2 文件跳过并返回 UNSUPPORTED_SCHEMA。
详情通过日期、会话 id、文件 revision 和 limit/cursor 读取，默认 100、最大 500 个阶段，
仍受 256 KiB 响应限制。完整契约见 [睡眠会话 v3](health-sessions-v3.md)。
网页仅读取 `index.json`，data 仅返回字符串 `title` 和 `url`，例如
`{"title":"示例网页","url":"https://example.com/article?from=clip#section"}`。
url 优先使用索引中的原始 url，缺失、非字符串或空白时使用 canonicalUrl；
保留原始查询参数与片段，不截断 URL。标题缺失时为空字符串。
不打开网页正文、不生成摘要、不依赖 captureCount 等其他展示元数据，
正文缺失或不可读不影响列表，也不再产生 DESCRIPTION_UNAVAILABLE。
网页按 capturedAt 的真实时间点倒序，同时间按完整 urlHash 倒序；无效时间排最后。

## 上限与短页

- 完整、未压缩的 UTF-8 JSON 响应最多 262144 字节，包含信封、游标、警告。
- 普通单项最多 64 KiB，健康整日项最多 240 KiB。
- 每请求原始日文件扫描最多 4 MiB；普通 JSONL 用 64 KiB 块读取，单行解码最多 1 MiB；健康 v3 按整文件校验，单文件最多 4 MiB。
- 通知、位置每页只返回一个日期文件的数据，不从下一天补足 limit；
  没有有效数据时每请求最多扫描 8 个日期文件。
- 日程沿请求方向跨日期文件尽量补足 limit（默认 50、最多 100 条），
  每请求最多扫描 64 个日期文件，空文件、坏行文件也占用文件预算。
  所有文件共用 4 MiB 扫描预算和 256 KiB 响应上限，达到任一上限即提前返回。
  健康每请求最多检查 14 个日文件。
- 网页冷读索引最多 32 MiB，不读取正文；仍受条数、单项和响应字节上限限制。V1 不缓存网页索引。
- 旧通知 JSON 数组最多读取 4 MiB；同日 JSONL 存在时优先读取 JSONL。
- 展示标题、应用名、发送人、会话名、日历名最多 512 个 Unicode 字符；
  通知 content、日程 description 最多 8192；地址、POI、日程地点、设备描述最多 1024。
  JSON 转义导致超限时继续缩短展示文本，记录 truncatedFields，原文件不变。
- URL、ID、时间、数字及健康指标不截断。仍超限则返回 status=oversized、
  data=null 的占位；知道原大小时返回 sourceBytes，占位计入 limit。
- 超大 JSONL 行可跨请求寻找边界，cursor 携带 skipLine，不保留行正文。
  找到完整行边界后返回 oversized 占位及原始字节数；来源字段不影响读取。

每页提前预留最大游标和警告空间，因此实际响应可能小于字节上限。
日期目录每次请求重新列举并排序，不缓存清单；4 MiB 限制的是正文扫描量，
不包含目录枚举，也不包含另行限制为 32 MiB 的网页索引读取。
尚未装入响应的合法记录不消费；下页仍从该记录开始。
空页或少于 limit 都不代表结束，只有 nextCursor=null 才结束。
成功响应的非空 cursor 必须推进逻辑位置。

响应头为 `Content-Type: application/json; charset=utf-8` 和 `Cache-Control: no-store`。

## Cursor 与一致性

cursor 为 Base64URL JSON 加 HMAC-SHA256 签名，绑定版本、类别、profile
和已解析查询。scopeHash 只包含 profile 的摘要，不绑定 clientLabel 或凭据。
同一 profile 的不同有效凭据可以继续使用同一个 cursor。
密钥和 bootId 在 daemon 实例中随机生成，不落盘。
有效期 60 分钟，生成下页 cursor 时续期；重启后旧 cursor 失效。
不同 bootId 只作为失效提示处理，不据此授权或打开任何文件。

| 存储 | 续读状态 |
| --- | --- |
| 通知、位置 JSONL | fileKey、fileId、snapshotEnd、beforeByte |
| 日程 JSONL | 上述文件身份，forward 用 afterByte，backward 用 beforeByte |
| 健康 | beforeDate，整日消费后推进 |
| 睡眠详情 | fileKey 日期、sessionId、文件 revision、nextIndex |
| 网页 | indexRevision、profile 完整索引排序后的 nextIndex |
| 旧通知数组 | fileKey、fileId、fileVersion、内容 revision、逆向 nextIndex |

日程一页跨多个文件时，cursor 仍只保存下一条待读记录所在文件及其字节位置，
不保存已读文件列表，也不增加索引。文件内达到上限时从当前文件续读；
恰好读完文件时捕获下一文件身份和快照，并将位置设为该方向的起始位置。
扫描文件数或字节预算耗尽时允许短页、空页，客户端继续沿 nextCursor 读取。

文件句柄和快照长度通过现有 store 写锁取得，之后只读已捕获范围。
正常追加保持身份，新增尾部通过刷新读取；文件替换、截断或删除返回 410。
健康读取前后检查文件版本，变化时重读一次，再次变化返回 409。
网页持已有全局写锁读取索引，索引变化使旧游标失效；仅正文变化不影响游标。

文件身份使用跨平台 os.SameFile，daemon 内保留最多 4096 份文件身份元数据，
不是每客户端会话。身份被淘汰也会使旧 cursor 返回 410。
JSONL/旧数组 recordId 可以随 daemon 重启或身份淘汰变化，刷新应替换列表。
支持的存储写入方式是追加和原子替换，不支持外部任意原地重写。
列表不是整个目录的全局快照，尚未进入的旧日期可以收到迟到上传。
日期文件只扫描目录第一层；拒绝类别根目录和日期文件符号链接，
网页索引同样拒绝符号链接，并通过 os.Root 读取类别目录内的 index.json。

## 鉴权和读取范围

一个 profile 只属于一个用户。五类资产接口沿用现有 gateway token、API-key、
Relay 内部鉴权；在既有 local 模式或通过鉴权后，读取当前 profile 的全部数据，
无需额外配置，也不按 clientLabel 筛选。通知的 clientLabel 作为来源信息返回；
网页不再返回来源元数据。其他来源、default 和无标签数据都包含在列表中（健康仅读取 v3 格式）。
cursor 仍绑定 profile；跨 profile 返回 CURSOR_SCOPE_MISMATCH，持有 cursor
不能绕过请求鉴权。该规则只适用于这五类新资产读取接口。
读取操作不修改用户配置或原始数据。

能力发现保留原 capabilities，并始终添加 assets.notifications.read.v1、
assets.web-pages.read.v1、assets.locations.read.v1、assets.calendars.read.v1、
assets.health-data.read.v1、assets.health-data.sleep.read.v1；/health 和 gateway health 使用同一份能力清单。

错误信封为 `{ok:false,error:{code,message,retryable,action}}`：

| HTTP | code | action |
| --- | --- | --- |
| 400 | INVALID_PARAMS、CURSOR_INVALID、CURSOR_QUERY_MISMATCH | none |
| 401 | UNAUTHORIZED | reauth |
| 403 | CURSOR_SCOPE_MISMATCH | none |
| 404 | SLEEP_SESSION_NOT_FOUND | refresh |
| 409 | SOURCE_CHANGED | retry |
| 410 | CURSOR_EXPIRED | refresh |
| 500 | SOURCE_READ_FAILED | retry |
| 503 | SOURCE_TOO_LARGE | none |

retryable 只在 action=retry 时为 true。真实读取失败不转换为空列表。
坏行警告按 code 合并并累计 count，与来源标签无关。

## 实现与测试

- internal/assets：参数/签名游标、文件身份、分块 JSONL 读取、字段投影、
  健康日读取、网页索引标题和 URL 投影，职责按文件拆分。
- internal/daemon/server_assets.go：原有 HTTP 入口接入、profile 读取、响应及完成日志。
- 各 store 的 read_view.go：与现有写锁协作，不改变上传行为。

测试集中在 internal/assets/*_test.go 和
internal/daemon/server_assets{,_relay}_test.go，覆盖：

- 千条通知、600 个日文件、50/50/23/8 跨日分页、相同时间戳、短页、空页推进。
- 所有类别默认/最大条数、非法参数、响应字节上限、不消费下一条。
- 64 KiB 块边界、1 MiB 行边界、CRLF、中文/emoji、无末尾换行、残缺尾行、坏 UTF-8。
- 正反向超大行跨请求续跳、4 MiB 扫描预算、通知及网页全部来源返回，通知来源字段保留。
- 追加快照、重复请求、刷新、覆盖/截断/删除、下一文件删除、身份缓存淘汰。
- 签名篡改、跨类别/跨 profile 拒绝、同 profile 跨凭据续读、到期、重启、路径逃逸。
- 日程方向/起点/时区、跨天事件、零点结束排他、快照替换。
- 日程稀疏多日期补足默认/最大条数、正反向文件中间/边界续读、64 文件预算、
  空文件和坏行推进、跨文件共用响应/扫描预算，以及 GET/POST 信封续读一致性。
- 健康完整日、256 项上限、不累计不补零、大项占位、旧格式、损坏、读中替换重试。
- 网页真实时间排序、索引版本、仅 title/url 投影、原始 URL 与 canonicalUrl 回退、
  正文路径忽略/正文删除后续页、Unicode 标题上限、超大 URL 占位、32 MiB 索引上限。
- HTTP/模拟 Relay 全部五类读取、不同来源标签、跨文件游标、能力发现、录音兼容、并发上传读取。

验证命令：

```sh
go test -race -p 1 ./...
go test -race ./internal/assets ./internal/daemon
go vet ./...
go test ./internal/assets -run '^$' -fuzz '^FuzzCursorDecoder$' -fuzztime=3s
```

所有写入测试使用临时目录、模拟 HTTP/Relay 和测试凭据。

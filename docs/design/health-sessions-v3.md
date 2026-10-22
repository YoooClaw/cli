# 健康快照与睡眠会话 v3

iOS、CLI 和资产页一起使用 v3，不兼容旧上传/读取格式。不会批量迁移或删除旧文件；
旧日期再次收到 v3 上传时，替换对应日期快照。其他日期保持原样，v1/v2 在资产列表中跳过并警告。

## 上报与存储

`POST /health-data` 沿用 ingest 鉴权；messageBridge 的 path 为 `health-data`，payload 为 JSON 字符串。
能力标记为 `health-data-daily-v3`。JSON 结构如下（示例为单阶段睡眠）：

```json
{
  "schemaVersion": 3,
  "provider": "healthkit",
  "timeZone": "Asia/Shanghai",
  "collectedAt": "2026-10-01T12:00:00.000+08:00",
  "days": [{
    "date": "2026-10-01",
    "items": [{"metric":"steps","name":"Steps","value":12000,"unit":"steps"}],
    "sleepSessions": [{
      "id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "startAt": "2026-09-30T23:00:00.000+08:00",
      "endAt": "2026-10-01T07:00:00.000+08:00",
      "sleepMinutes": 480,
      "stageMinutes": {"core":480,"deep":0,"rem":0,"unspecified":0},
      "awakeMinutes": 0,
      "awakeningCount": 0,
      "longestAwakeMinutes": 0,
      "unknownMinutes": 0,
      "isPartial": false,
      "sources": [{"id":"watch","name":"Test Watch"}],
      "segments": [{
        "stage":"core",
        "startAt":"2026-09-30T23:00:00.000+08:00",
        "endAt":"2026-10-01T07:00:00.000+08:00",
        "source":"watch"
      }]
    }]
  }]
}
```

- `items` 和 `sleepSessions` 必须为数组，可以分别为空，但不能同时为空。睡眠不再存于 `sleep_*` 指标。
- 每请求 1–366 个不重复日期，每日最多 256 个普通指标、32 次睡眠；单次睡眠 1–4096 个阶段、1–32 个来源。
  来源 id/name 各不超过 512 UTF-8 字节。单日 JSON 请求项最多 `4 MiB - 4096`，落盘最多 4 MiB。
- iOS 重算最近 30 个自然日，额外向前查询 36 小时睡眠，保护查询窗口第一天的跨夜记录。
  上传按完整日期分批，每批原始 JSON 最多 8 MiB；不拆开同一天、同一次睡眠，也不截断时间线。
- `collectedAt` 固定为查询开始时刻，睡眠不超过该时刻；超过 CLI 当前时间 24 小时的采集时间被拒绝。
- CLI 校验整批后，每日期原子替换；更旧采集时间不能覆盖更新数据，多日期写入失败可幂等重试。
- 无可见数据可能是权限原因，不上传空快照来删除历史。仅剩睡眠的日期同样上传。
  若某日期之后完全不可见，历史文件不会自动删除；缺少字段不是零值。
- 收据 `ingested` = 普通指标数 + 睡眠会话数，阶段不重复计数；`deleted` 为 0。

`profiles/<profile>/health-data/YYYY-MM-DD.jsonl` 第一行为
`schemaVersion/date/timeZone/updatedAt/provider`；普通指标每项一行，完整睡眠每次一行：
`{"type":"sleepSession","session":{...摘要字段与完整segments...}}`。
目录 README 会写明 Agent 阅读规则，避免把来源、日期快照或总数与阶段再次累加。

## 会话与统计规则

1. 普通指标按自然日。完整睡眠归 `endAt` 在上传时区所属日期，跨午夜不拆分，同日可有多次睡眠。
2. 先将时间精度固定到毫秒。按开始、结束、来源标识确定来源顺序；优先来源占据重叠部分，
   后续来源只填空白。同源详细阶段优先于 unspecified，inBed 不覆盖阶段。
3. 实际睡眠为 core/deep/rem/unspecified；相邻实际睡眠间隔不超过 60 分钟合为一次，超过则另起一次。
   不要求以 core 开始。会话始末分别为首个、末个实际睡眠片段的边界，不等于上床或确认起床。
4. `segments` 连续且有序；已观察清醒为 awake，缺测间隔补 unknown，unknown 无 source，不算睡眠或清醒。
   开头和末尾的 awake/inBed 不算入会话。
5. 每个有效片段起止时间分别四舍五入到分钟（30 秒进位），相减后按阶段汇总。
   `sleepMinutes` 等于四种实际睡眠阶段分钟数之和；保留原始毫秒时间线，短片段可统计为 0 分钟。
6. `awakeningCount` 为连续原始 awake 区间次数，即便取整后为 0 仍计数；`longestAwakeMinutes` 用同样分钟规则。
7. `isPartial` 只表示触及查询边界；false 不保证设备已同步完整或用户已起床。
8. iOS 会话 id 是起始时间毫秒与排序后的来源标识的 SHA-256；起点或来源校正时可能改变，必须整日替换。
   CLI 不替客户端决定来源优先级，但会重算分钟数并校验摘要与完整时间线一致。

这些是产品统计规则；起止取整规则匹配已核验的健康 App 对照，不声称复刻 Apple 的私有统计算法。

## 健康列表

`GET /assets/health-data?limit=7&cursor=...` 请求及日期 cursor 规则沿用资产列表，默认 7 天、最多 14 天。
完整响应最多 256 KiB，单日展示项最多 240 KiB，每请求文件读取预算 4 MiB，可能返回短页。
只有 `nextCursor:null` 表示结束。普通项及睡眠摘要作为完整日期一起返回，不按会话分页。

每个 `items[].data` 包含 `schemaVersion:3/date/timeZone/provider/updatedAt/revision/items/sleepSessions`。
`revision` 是该日期文件内容 SHA-256。`sleepSessions` 保存以上全部摘要字段，
以 `segmentCount` 替代 `segments`。因此列表不传阶段时间线，客户端按需打开详情。

## 睡眠详情

`GET /assets/health-data/{date}/sleep-sessions/{id}?revision={revision}&limit=100&cursor=...`

- date 是列表返回的业务日期，id 为该摘要的 64 位小写十六进制 id。
- 首次必填 revision（64 位小写十六进制），续页可省略；重复传递必须与 cursor 一致。
- limit 默认 100，最大 500，必须为正十进制整数；越界、未知/重复参数均返回 400。续页可调整 limit。
- 支持与列表相同的 POST 只读信封，统一登录、API-key、profile 鉴权，全部来源可见。
- 能力标记 `assets.health-data.sleep.read.v1`，列表能力仍为 `assets.health-data.read.v1`。

```json
{
  "ok": true,
  "date": "2026-10-01",
  "revision": "<64位文件内容摘要>",
  "session": {"...": "列表中的同一睡眠摘要，含segmentCount，不含segments"},
  "items": [{"stage":"core","startAt":"...","endAt":"...","source":"watch"}],
  "nextCursor": "<不透明游标或null>"
}
```

详情 items 顺序与存储时间线一致。完整响应最多 256 KiB，达到字节上限返回短页，
未加入响应的片段不会消费；成功页至少包含一个片段。不包含总页数或额外目录统计。

cursor 继续复用 HMAC 签名，绑定 profile、daemon 实例、类别、日期 fileKey、sessionId、
文件 revision 和下一片段下标 nextIndex。每次直接打开指定日期文件，无需跨文件查找或新增索引。
同一 profile 的有效凭据可续读。有效期 60 分钟，续页续期；过期、daemon 重启、文件内容替换返回 410。
旧列表 revision 的首次详情读取同样返回 410，防止新旧摘要与阶段混用；回健康列表刷新。
路径与日期严格验证，拒绝符号链接。读中变化重试一次，仍不稳定返回 409；读取失败返回 500，
文件超 4 MiB 返回 503，文件不可用可能返回 409，已有有效文件但不存在该会话返回 404
`SLEEP_SESSION_NOT_FOUND` / `action:refresh`。其他错误信封沿用资产规范。

## 验证

- iOS 包：跨午夜/结束日、60 分钟边界、午睡、多来源覆盖、inBed、unknown、0 分钟清醒、毫秒与分钟边界、超限失败。
- 上传与资产模型：完整时间线编码、睡眠单独成日、固定采集时刻、整日分批、详情翻页、重复游标、过期清空、迟到响应与并发保护。
- CLI：v3 完整存储/替换、摘要一致性、阶段与来源边界、默认/最大条数、256 KiB 短页、跨页无遗漏、revision/profile/query 绑定、篡改/过期/重启、鉴权及 GET/POST 信封。
- 原始健康数据只用于本机隔离校验，不作为公开测试夹具提交。

# HealthKit 扩展采集与 v3 明细

健康上报继续使用 `POST /health-data`，手机通过现有统一 API 的 messageBridge 转发。
外层仍为 `schemaVersion: 3 / provider / collectedAt / timeZone / days`；
`days[].date / items / sleepSessions` 保持原义。`items` 每项仍严格只有
`metric / name / value / unit`，不混入 HealthKit 类名或原生 metadata。

## 可选每日明细

仅非空时上报下列数组；旧 v3 请求无需提供它们。完整合成样例位于
`internal/healthdata/testdata/health-details-v3.json`。

| 字段 | 每项字段 | 归属日期与说明 |
| --- | --- | --- |
| medications | id、name、可选 nickname、form、isArchived、hasSchedule、doseEvents | 当天含全部用户共享的当前药物；历史日期仅含该日有事件的药物。名称、归档与计划标记均为 collectedAt 时当前状态，不代表历史处方 |
| medications[].doseEvents | id、startAt、endAt、status、scheduleType、可选 scheduledAt、scheduledQuantity、quantity、unit | 按 startAt 所在本地日期；计划时间独立保留，不用它覆盖记录时间 |
| workouts | id、activity、name、startAt、endAt、durationMinutes、items | 按开始日期；有效运动时长保留暂停语义，不以结束减开始替代。items 为系统已有的该次运动心率、能量、步跑/骑行距离统计 |
| intakeRecords | id、startAt、endAt、metric、name、value、unit | 饮水与咖啡因来源明细，按开始日期；不能与 items 的系统合并日总量重复相加 |
| moods | id、startAt、endAt、kind、valence、labels、associations | 保留逐条心情，不取每日平均。kind 为 momentary_emotion 或 daily_mood，valence 为 -1..1 |

用药从 iOS 26 起读取，使用系统逐药物选择授权，不申请写入。
status 保留 taken、skipped、snoozed、not_interacted、notification_not_sent、not_logged；
未来未知系统值用 unknown_数字，不把未交互/未记录当作漏服。
scheduleType 为 scheduled 或 as_needed。scheduled 的事件同时保留计划时间和计划数量；
quantity 为系统记录的服用数量，可缺失，不能自行补成 0 或计划数量。
药物 id 为不透明概念标识的安全归档摘要，不用可修改的药物名作为身份；整日快照替换，不跨版本累加。

明细的 value、durationMinutes、valence 和药物 isArchived/hasSchedule 必须提供。
缺失或 null 返回具体字段名；真实的 0 和 false 保留。可选剂量仍允许缺失。

## 新增日指标

| metric 基名 | 统计 | 单位 |
| --- | --- | --- |
| basal_energy | 日合计 | kcal |
| stand_minutes | 日合计 | min |
| walking_heart_rate | latest | bpm |
| heart_rate_recovery_one_minute | latest | beats |
| vo2_max | latest | mL/kg/min |
| walking_speed | average | m/s |
| walking_step_length | average | m |
| walking_steadiness | latest | %（0..100） |
| dietary_water | 日合计 | mL |
| dietary_caffeine | 日合计 | mg |
| dietary_energy | 日合计 | kcal |
| dietary_protein / dietary_carbohydrates / dietary_fat | 日合计 | g |
| mindful_minutes | 本地日期内区间并集 | min |

latest / average 延续现有命名规则，在 metric 基名后增加 `_latest` / `_average`。
数量统计由 HealthKit 完成，不把不同来源原始值自行相加。
心率恢复直接读取系统 count 类型结果，不能按 bpm 读取；VO2 max 为系统已有值，不自行估算。

活动摘要直接提供 `stand_hours`、可用的 `stand_hours_goal`、`exercise_minutes_goal`，
以及能量模式下的 `active_energy_goal`；时间模式下提供 `apple_move_minutes / apple_move_minutes_goal`。
`activity_rings_paused` 以 boolean 单位的数值 0/1 保留系统暂停标记。
站立小时为达标小时数，不是累计站立时长。未返回的目标不补零。

## 授权、同步与限制

- 不增加更新提示、红点或自动授权弹窗。用户在原资产设置健康入口补充授权；
  iOS 26 的首次用药授权随后进入系统药物选择。仅记录是否完成过选择页，不缓存读取授权结论。
- 新类型加入原权限列表、变化观察与后台交付，仍共用 30 天窗口、前台 60 秒/observer 1 小时冷却。
- 无数据不代表零或拒绝；无任意可读指标/明细的日期不上传。请求异常不发送部分日期结果。
- 每日最多 128 药物、4096 服药事件、128 运动、4096 摄入记录、1024 心情；
  每次额外样本查询最多 20000 条，超过时整轮失败，不静默截断。
- 日文件继续最多 4 MiB，整日不可拆分。超出上限只使该日失败，不影响同批其它日期。
  回执 ingested 按普通指标、睡眠会话、药物分组、单次运动、摄入记录、心情记录各计一项；
  药物分组内的 doseEvents 不重复计数；被丢弃的记录不计入 ingested。

## CLI 存储、读取与兼容

新 CLI 保留旧 v3 请求接收能力。旧 CLI 严格拒绝未知字段，不能接收这些可选扩展，
因此需要先更新 CLI 源码对应版本，再使用新手机构建；能力声明增加 `health-data-details-v1`。
本协议不要求修改 App 的显示版本号，也不触发数据迁移。

日 JSONL 继续先写原 header、普通指标及 type=sleepSession/session；新增四种可选行：
`type=medications/workouts/intakeRecords/moods`，`records` 为完整数组。
整批校验后按日期原子替换。覆盖顺序见下文“每日来源优先级”。

`health-data get --date YYYY-MM-DD` 返回完整明细。
默认 list、记忆批次和资产列表保留运动记录，用药返回元数据、事件数与最新事件；
饮水/咖啡因、心情返回记录数与最新记录，避免事件量使列表膨胀。
完整明细均参与内容版本，单独修改一次服药事件也会被后续记忆批次检测到；
没有新明细的旧日期保持原内容版本计算，不触发历史重消费。
手机资产页现有数值展示增加名称/单位映射，本轮不新增用药等明细页面。

运动 durationMinutes 保留厂商上报值。
厂商取整口径不同，不再与起止时间差比较上限。
仍校验时长非负且有限、结束时间不早于开始时间。

## 每日来源优先级

按日期选择整份快照，不跨来源拼接字段。
provider 去除首尾空白并转为小写后保存。
新上报和已有快照都按归一后的 provider 判断来源。
`provider=healthkit` 表示苹果，其余非空来源归为非苹果类。
provider 是开放标识，不设置厂商白名单；它不构成来源真实性认证。
苹果可覆盖非苹果快照，不要求采集时间更新。
该日期已有苹果快照时，忽略非苹果上报。
安卓厂商与鸿蒙之间可以相互覆盖。
同类按 collectedAt 比较，更旧的上报不覆盖。
时间相等时，沿用后处理的上报覆盖。
优先级仅限该日期，不影响其他日期的来源。
被忽略的上报不改文件，不产生新的数据版本。

## 健康上报回执

请求级错误（JSON 无法解析、schemaVersion/provider/collectedAt/timeZone 不合法、
days 为空或超过 366）整请求返回 400，不写任何日期。

逐日校验独立，且失败按“记录 → 类别 → 天”逐级降级，尽量少丢数据：

1. 记录级：单条指标、睡眠会话、药物分组、服药事件、运动、运动内指标、摄入记录、
   心情、心情标签/关联校验失败时只丢弃该条记录，同一天其它记录照常写入。
   数量超限时保留前 N 条并丢弃其余；重复指标或重复会话保留首条。
2. 类别级：可选明细类别（`medications`/`workouts`/`intakeRecords`/`moods`）整体不是数组时
   丢弃该类别（按空数组落盘），同一天其它类别继续处理。`items` 与 `sleepSessions`
   是必填类别，结构错误按天级处理，避免一次格式错误抹掉整类历史。
3. 天级：`items` 或 `sleepSessions` 缺失、为 null 或不是数组（整类删除语义不明）、
   日期非法或重复、丢弃后该日已无任何有效记录、单日编码超过 4 MiB 时，该天不写入。
   同请求中其它合法日期照常写入。

`POST /health-data` 返回 `ok`、`ingested`、`deleted`、`rejected`。
`ingested` 只统计校验通过并保留的记录数，不含被丢弃的记录，也不表示实际落盘数；
`rejected` 表示天级校验失败或已有文件不可识别而未写入的天数；`deleted` 仍为 0。
`days` 数组按请求日期顺序返回写入决定：

```json
{"ok":true,"ingested":2,"deleted":0,"rejected":1,"days":[{"date":"1997-01-01","applied":false,"reason":"apple_priority"},{"date":"1997-01-02","applied":true,"dropped":1,"warnings":["days[0].workouts[0]: has invalid fields"]},{"date":"1997-01-03","applied":false,"reason":"invalid_snapshot","message":"days[2]: no valid records remain after dropping invalid records: days[2].items[0]: has invalid metric fields","dropped":1,"warnings":["days[2].items[0]: has invalid metric fields"]}]}
```

- `applied:true`：采用该日快照。幂等替换也返回 true，不代表版本变化。
  若同时返回 `dropped`/`warnings`，表示该日已写入，但其中若干记录被单独丢弃。
- `applied:false`、`reason:apple_priority`：已有苹果快照，忽略非苹果数据。
- `applied:false`、`reason:stale_collected_at`：同类来源的采集时间更旧。
- `applied:false`、`reason:invalid_snapshot`：该日未通过天级校验；`message`
  给出具体原因，包含 `days[下标]` 和出错字段，不含指标值；`dropped`/`warnings`
  说明在降级过程中丢弃了哪些记录。
- `applied:false`、`reason:unreadable_snapshot`：该日已有文件不是本版本可识别的
  v3 快照，为避免覆盖未知数据而跳过；该文件保持不变，其它日期继续写入。

`dropped` 为被丢弃的记录数，跨类别累计，计数精确；
`warnings` 逐条给出丢弃原因，每天最多 20 条，超出时追加 `(+N more)`。
两者都是省略字段，没有丢弃时不出现。

返回 200 且 `ok:true` 表示请求已处理，不代表所有日期和记录都已写入；
客户端应同时检查 `rejected`、`ingested` 与 `days[].applied`/`dropped`。
跳过与校验失败都属于可解释结果，无需原样重试（原样重试只会得到同一结果）。
日志逐日记录 `applied`、`reason`、`message`、`dropped` 与 `warnings`；
有记录被丢弃或整日失败时使用 WARN 级别。

文件级 I/O 失败（目录不可写、磁盘错误）仍返回 500；此前已完成的日期决定保留在日志中。
多日期写入不是跨日期事务，客户端可按原请求幂等重试。

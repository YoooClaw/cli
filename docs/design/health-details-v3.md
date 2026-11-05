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
- 日文件继续最多 4 MiB，整日不可拆分。回执 ingested 按普通指标、睡眠会话、药物分组、
  单次运动、摄入记录、心情记录各计一项；药物分组内的 doseEvents 不重复计数。

## CLI 存储、读取与兼容

新 CLI 保留旧 v3 请求接收能力。旧 CLI 严格拒绝未知字段，不能接收这些可选扩展，
因此需要先更新 CLI 源码对应版本，再使用新手机构建；能力声明增加 `health-data-details-v1`。
本协议不要求修改 App 的显示版本号，也不触发数据迁移。

日 JSONL 继续先写原 header、普通指标及 type=sleepSession/session；新增四种可选行：
`type=medications/workouts/intakeRecords/moods`，`records` 为完整数组。
整批校验后按日期原子替换，较旧 collectedAt 不覆盖新快照。

`health-data get --date YYYY-MM-DD` 返回完整明细。
默认 list、记忆批次和资产列表保留运动记录，用药返回元数据、事件数与最新事件；
饮水/咖啡因、心情返回记录数与最新记录，避免事件量使列表膨胀。
完整明细均参与内容版本，单独修改一次服药事件也会被后续记忆批次检测到；
没有新明细的旧日期保持原内容版本计算，不触发历史重消费。
手机资产页现有数值展示增加名称/单位映射，本轮不新增用药等明细页面。

# Voice 与输入法会议录音查询重构：从 SQLite 切换到每日 JSONL

状态：已实现
目标读者：YoooClaw CLI、桌面语音输入 App 与 Agent Skill 维护者
最后更新：2026-08-21

## 1. 决策摘要

本次重构采用以下确定方案：

1. 短语音输入历史只读取当前 profile 下的 `voice/audio-jsonl/YYYY-MM-DD.jsonl`。
2. 不再读取、探测、迁移或回退到 `voice.sqlite3`，旧 SQLite 文件保留原状但由 CLI 忽略。
3. 查询模型参考通知查询：按日期倒序枚举文件、按时间范围裁剪、逐条过滤、达到 `limit` 后尽早停止、坏行隔离。
4. JSONL 中的数字 `id` 继续视为输入法数据库内部主键；`voice_id` 是 CLI 对外使用的稳定语音事件 ID，并与音频文件 stem 一致。
5. `--app` 只对桌面 App 的完整 `app_name` 与 `app_id` 做不区分大小写的精确匹配；不维护 CLI 别名或类别扩展。
6. Agent 在用户指定 App 时先调用 `voice apps` 获取真实身份，再优先使用明确的 `app_id` 查询，避免 `app_name` 与产品名不一致。
7. `voice apps` 默认扫描滚动最近 7 天，按 `app_id` 去重并同时返回 `app_id`、`app_name`、计数和最新时间；`--all` 才显式扫描全历史。
8. 下线 `voice stats`。本次不设计 usage JSONL，也不从历史 JSONL 反推用量。
9. 输入法会议录音读取 `voice/recordings-jsonl/YYYY-MM-DD.jsonl`；每条录音只写一行最终索引，产物位于 `voice/recordings/YYYY/MM/<recording-id>/`。
10. CLI 不接管会议录音状态机，也不根据 `status` 推断产物可用性；是否存在音频、转写和摘要，以安全解析后的本地文件事实为准。
11. 统一 `recording` 查询同时返回两种来源：电脑软件 **YoooClaw Capture** 和 **YoooClaw 智能硬件**；每条结果必须携带稳定来源类型与可读来源名称。
12. 第一版不引入持久化搜索索引或 App sidecar。JSONL 是唯一事实来源；未来如有性能需要，只允许增加可删除、可重建的派生索引。

## 2. 背景

当前 CLI 通过 `internal/voice.Repository` 只读打开：

```text
~/.yoooclaw/profiles/<profile>/voice/voice.sqlite3
```

并依赖以下数据库对象：

```text
agent_voice_history_v1
agent_voice_usage_daily_v1
usage_daily（旧版兼容）
```

为了安全读取一个由桌面 App 持续写入的 SQLite，CLI 还承担了只读 URI、`query_only`、WAL、busy timeout、稳定 View 字段检查和版本不兼容错误等职责。

桌面语音输入 App 现在已经按本地日期写出短语音输入与会议录音索引：

```text
~/.yoooclaw/profiles/<profile>/voice/
├── audio-jsonl/
│   ├── 2026-08-03.jsonl
│   ├── 2026-08-04.jsonl
│   └── 2026-08-20.jsonl
├── audio/
│   └── YYYY/MM/<voice-id>.wav
├── recordings-jsonl/
│   └── 2026-08-20.jsonl
└── recordings/
    └── YYYY/MM/<recording-id>/
        ├── audio.m4a
        ├── transcript.json
        └── summary.md
```

短语音历史是追加型事件，一次语音输入对应一行 JSON。会议录音索引也是每日 JSONL，但一行描述一条会议录音及其多个最终产物，不是状态事件流。两者都与通知的每日 JSONL 查询模型一致，适合作为桌面 App 与 CLI 之间的只读数据契约。

## 3. 目标与非目标

### 3.1 目标

- CLI 查询完全脱离 SQLite 和写入 App 的内部数据库结构。
- 保持现有历史查询的核心语义和主要输出字段稳定；对外 ID 切换为 `voice_id` 是本次明确的 breaking change。
- 支持 App、关键词、状态、语言、时间和本地音频过滤。
- 对持续追加、末尾半行和单条坏数据具有容错能力。
- 最近、今日和时间范围查询只读取必要的日期文件。
- App 解析使用桌面端真实的名称和 bundle ID，不猜测产品别名。
- 使用 `voice_id` 稳定关联短语音历史与可选 WAV 文件。
- 让现有 `recording` 查询能够发现输入法端会议录音、转写和摘要。
- 让 Agent 能稳定区分来自电脑软件 YoooClaw Capture 与来自 YoooClaw 智能硬件的录音。
- 向 Agent 暴露会议录音各产物当前真实的可用性与缺失项。
- 数据规模增长到数年时，常用的最近查询仍保持稳定。

### 3.2 非目标

- 不由 CLI 采集麦克风、执行 ASR 或向目标 App 注入文本。
- 不修改或修复桌面 App 写出的 JSONL。
- 不迁移旧 SQLite 数据，也不提供 SQLite fallback。
- 不在第一版构建全文索引、App 索引或每日 meta 文件。
- 不保留 `voice stats` 或另建用量统计数据源。
- 不由 CLI 管理输入法会议录音的录制、转写或摘要状态。
- 不通过 `status` 猜测产物是否存在，也不修复缺失产物。
- 不要求 `transcript.json` 的 ASR `task_id` 等于录音 `id`。
- 不保证全历史关键词搜索和 `voice apps --all` 在任意数据量下都是常数时间。

## 4. 数据契约

### 4.1 路径与分片

历史目录固定为：

```text
<profile>/voice/audio-jsonl/
```

CLI 只识别严格匹配以下格式的普通文件：

```text
YYYY-MM-DD.jsonl
```

文件名日期必须是该记录 `started_at` 在采集设备当时本地时区下的自然日。生产端应将同一自然日的记录追加到同一个文件。

第一版继续使用每日分片，不合并成一个永久增长的总文件。每日分片可以精准裁剪今日、最近 72 小时和日期范围，同时限制坏文件的影响范围。未来如果小文件数量成为真实瓶颈，可以单独评估月度分片，不在本次重构中改变生产端格式。

### 4.2 单行结构

每行是一个完整 JSON 对象，并以 `\n` 结束：

```json
{
  "id": 123,
  "voice_id": "9b132b00c5d961c1-1787206537256",
  "started_at": "2026-08-18T16:56:33+08:00",
  "ended_at": "2026-08-18T16:56:40+08:00",
  "timezone_offset_min": 480,
  "duration_ms": 7000,
  "platform": "macos",
  "app_id": "com.bytedance.macos.feishu",
  "app_name": "飞书",
  "window_title": "...",
  "text": "...",
  "language": null,
  "char_count": 12,
  "result_status": "success",
  "audio_rel_path": "audio/2026/08/9b132b00c5d961c1-1787206537256.wav"
}
```

字段约束：

| 字段 | 类型 | 约束 |
|---|---|---|
| `id` | integer | 输入法数据库内部主键；CLI 不把它作为稳定对外 ID |
| `voice_id` | string | 必填；一次语音输入的稳定外部 ID，在一个 profile 内唯一 |
| `started_at` | string | 必填，带时区 RFC3339 |
| `ended_at` | string | 必填，带时区 RFC3339 |
| `timezone_offset_min` | integer | 记录发生时的时区偏移事实 |
| `duration_ms` | integer | 大于等于 0，以生产端记录为准 |
| `platform` | string | 如 `macos`、`windows` |
| `app_id` | string | 桌面 App bundle/application ID，可为空但字段应存在 |
| `app_name` | string | 用户可见 App 名称，可为空但字段应存在 |
| `window_title` | string/null | 可选敏感信息 |
| `text` | string | 最终上屏文本；失败记录允许为空 |
| `language` | string/null | 识别语言，可空 |
| `char_count` | integer | 大于等于 0，以生产端记录为准 |
| `result_status` | string | 输入结果状态 |
| `audio_rel_path` | string/null | 相对 `<profile>/voice` 的音频路径 |

Reader 必须忽略未知字段，允许生产端以后增加可选字段。Reader 不根据时间差重算 `duration_ms`，也不根据文本长度重算 `char_count`。

`voice_id` 与音频的关联契约：

- `voice_id` 在一次语音输入开始时生成，与数据库自增主键的生命周期无关。
- 即使没有保存音频、`audio_rel_path` 为 `null`，记录也必须有 `voice_id`。
- 保存音频时，文件名必须是 `<voice_id>.<ext>`；当前格式为 WAV。
- `voice_id` 不包含扩展名和目录，不能从 `audio_rel_path` 临时拆解生成。
- `voice_id` 一旦写入不可改变、不可复用；数字 `id` 只用于生产端内部兼容。

CLI 对外输出统一把 `voice_id` 暴露为历史项的 `id`，不输出数据库数字主键。例如源数据：

```json
{"id":64,"voice_id":"9b132b00c5d961c1-1787206537256"}
```

CLI 输出：

```json
{"id":"9b132b00c5d961c1-1787206537256"}
```

### 4.3 写入约束

生产端应满足：

- 以 append 模式写入。
- 一条记录序列化为一个 buffer，并将 JSON 和末尾换行一次性追加。
- 同一个 profile/日期文件只有一个逻辑 writer。
- 不在原地修改已经完成的历史行。
- 每条记录必须有唯一且稳定的 `voice_id`；保存音频时由同一个 `voice_id` 派生文件名。
- 如果需要更正、删除或 tombstone，必须另行设计版本化事件，本次不定义。

### 4.4 输入法会议录音契约

会议录音索引目录固定为：

```text
<profile>/voice/recordings-jsonl/YYYY-MM-DD.jsonl
```

每条会议录音只追加一行最终索引，不为同一个 ID 追加状态更新行。当前真实样本结构为：

```json
{
  "id": "531e3f9f-37c3-4c86-8896-914b263986b2",
  "title": "...",
  "audio_rel_path": "recordings/2026/08/531e3f9f-37c3-4c86-8896-914b263986b2/audio.m4a",
  "transcript_rel_path": "recordings/2026/08/531e3f9f-37c3-4c86-8896-914b263986b2/transcript.json",
  "summary_rel_path": "recordings/2026/08/531e3f9f-37c3-4c86-8896-914b263986b2/summary.md",
  "recorded_at": "2026-08-20T14:21:05+08:00",
  "duration_ms": 134340,
  "status": "completed"
}
```

字段约束：

| 字段 | 类型 | 约束 |
|---|---|---|
| `id` | string | 必填，稳定的录音 UUID；同时作为录音产物目录名 |
| `title` | string | 录音显示标题 |
| `recorded_at` | string | 必填，带时区 RFC3339 |
| `duration_ms` | integer | 大于等于 0，以生产端记录为准 |
| `status` | string | 生产端原始状态，可展示但不驱动文件可用性判断 |
| `audio_rel_path` | string/null | 相对 `<profile>/voice` 的音频路径 |
| `transcript_rel_path` | string/null | 相对 `<profile>/voice` 的结构化转写路径 |
| `summary_rel_path` | string/null | 相对 `<profile>/voice` 的 Markdown 摘要路径 |

录音 ID 标识整次会议录音，而不是单个音频文件。一个 ID 对应一个目录，目录内允许有多个产物，因此无需要求 `audio.m4a` 文件名再重复 ID。

`transcript.json` 当前结构为：

```text
task_id
title
category
transcripts[]
└── sentences[]
    ├── begin_time
    ├── end_time
    ├── speaker_id
    └── text
```

其中 `begin_time` / `end_time` 为毫秒；读取正文时按时间顺序拼接 `sentences[].text`。`task_id` 是 ASR 任务 ID，不是录音 ID，二者不要求相等。`speaker_id` 应保留供未来多说话人展示，不能假定当前只有 `0`。

### 4.5 会议录音产物事实

查询层不管理会议录音状态机。JSONL 提供索引和声明路径，文件系统提供当前事实：

```json
{
  "id": "531e3f9f-37c3-4c86-8896-914b263986b2",
  "status": "completed",
  "has_audio": true,
  "has_transcript": false,
  "has_summary": true,
  "audio_path": "/absolute/path/audio.m4a",
  "transcript_path": null,
  "summary_path": "/absolute/path/summary.md",
  "missing_artifacts": ["transcript"]
}
```

判定规则：

- 相对路径为空：产物未声明，`has_* = false`。
- 相对路径安全且目标为当前存在的普通文件：`has_* = true`，返回绝对路径。
- 声明了路径但文件不存在：`has_* = false`，加入 `missing_artifacts`。
- 路径越界、符号链接逃逸或目标不是普通文件：按不可用处理，不暴露绝对路径。
- `status` 原样展示，但绝不能替代上述文件检查，也不阻止返回一条索引记录。

## 5. 当前桌面 App 身份样本

2026-08-18 对本机 `audio-jsonl` 的一次只读扫描共看到 58 条记录，全部为 `macos`。此表说明为什么候选列表必须同时返回真实名称和 ID；它不是允许列表。

| `app_name` | `app_id` | 样本数 |
|---|---|---:|
| ChatGPT | `com.openai.codex` | 27 |
| Code | `com.microsoft.VSCode` | 7 |
| Google Chrome | `com.google.Chrome` | 6 |
| 微信 | `com.tencent.xinWeChat` | 6 |
| Xcode | `com.apple.dt.Xcode` | 5 |
| Microsoft Edge | `com.microsoft.edgemac` | 3 |
| 飞书 | `com.bytedance.macos.feishu` | 1 |
| 飞书 | `com.electron.lark` | 1 |
| 企业微信 | `com.tencent.WeWorkMac` | 1 |
| WorkBuddy | `com.workbuddy.workbuddy` | 1 |

样本揭示了三个不能只匹配显示名的情况：

- VS Code 的显示名是 `Code`。
- ChatGPT 的 bundle ID 是 `com.openai.codex`。
- 飞书存在多个桌面 bundle ID，不能只按显示名去重，否则 Agent 看不到真实身份差异。

2026-08-20 的新增样本还验证了身份契约：`voice_id` 为
`9b132b00c5d961c1-1787206537256`，对应音频文件正是
`audio/2026/08/9b132b00c5d961c1-1787206537256.wav`；数据库数字 `id` 为 `64`，不参与对外关联。

## 6. App 身份发现与匹配

### 6.1 `--app` 精确匹配

`--app <value>` 对每条记录只做两项判断：

1. `strings.TrimSpace` 后，对完整 `app_id` 做不区分大小写匹配。
2. 对完整 `app_name` 做不区分大小写匹配。

CLI 不做别名、类别、模糊或子串扩展。例如源记录为
`{"app_id":"com.microsoft.VSCode","app_name":"Code"}` 时，`--app Code` 和
`--app com.microsoft.VSCode` 可以命中，`--app "VS Code"` 不会被 CLI 猜测为同一 App。

### 6.2 `voice apps` 默认范围

无 `--from/--to` 且未传 `--all` 时，`voice apps` 使用查询时刻的滚动最近 7 天
`[now-7d, now)`。返回中带 `default_range_applied: true`、准确 `range` 和提示。

- `--from/--to`：扫描调用方明确指定的范围。
- `--all`：明确扫描全部 JSONL 历史。
- 不设置默认条数上限。

### 6.3 去重与输出

有 `app_id` 的记录以规范化后的 `app_id` 为去重键；同一 ID 使用最近一次非空
`app_name` 作为展示名称。`app_id` 为空时，退化为按规范化 `app_name` 去重。
不同 `app_id` 即使 `app_name` 相同也分别返回。

```json
{
  "app_id": "com.microsoft.VSCode",
  "app_name": "Code",
  "history_count": 7,
  "latest_at": "2026-08-18T16:56:33+08:00"
}
```

同时返回两个身份字段，正是为了让 Agent 识别 `app_name = Code` 实际对应 VS Code，
然后把稳定且明确的 `com.microsoft.VSCode` 传给后续 `--app` 查询。

### 6.4 Agent 解析流程

```text
用户 App 用词 → voice apps → Agent 对照 app_id/app_name → voice search/list --app <精确身份>
```

普通近期查询使用默认 7 天候选；显式历史时间范围可将相同 `--from/--to` 传给
`voice apps`。若多个真实身份都可能符合用户用词，Agent 展示 ID/名称让用户选择，
不能由 CLI 维护一份可能过期的桌面别名表。

## 7. 查询算法

### 7.1 文件枚举

1. 在查询开始时列出 `audio-jsonl` 的目录快照。
2. 只接受 `^\d{4}-\d{2}-\d{2}\.jsonl$`。
3. 提取日期 key，按字符串降序排列。
4. 根据查询的 `from`/`to` 日期范围跳过不可能命中的文件。
5. 对显式带时区的瞬时时间边界，文件裁剪应保守包含边界前后相邻日期，再用每条记录解析后的时间做最终判断，避免时区差异造成漏查。

### 7.2 单文件读取

为兼容活跃 writer：

1. 打开文件后记录当时的文件大小。
2. 本次查询只读取该大小以内的快照，不追逐查询期间新追加的数据。
3. 逐行解码或读取整天快照后按换行切分；不能使用默认 64 KiB 上限且不可配置的 Scanner。
4. 空行忽略。
5. 没有末尾换行的最后一段视为尚未完成，本次忽略。
6. 中间的非法 JSON 或字段校验失败记录跳过并计数，不让单条坏记录使整次查询失败。
7. 文件在目录枚举后被并发删除时可以跳过；其他读取错误返回存储不可用错误，不能静默伪装成空结果。

生产数据在查询过程中可能继续增长；查询只承诺返回开始扫描时可见的一个近似快照，不承诺跨多个日期文件的数据库级事务快照。

### 7.3 排序与早停

生产端通常按发生顺序追加，但 CLI 不依赖此假设。每个日期文件解码后按以下顺序稳定排序：

```text
started_at DESC, voice_id DESC
```

日期文件本身从新到旧扫描，因此匹配结果天然按时间倒序。达到调用方的 `limit` 后可以立即停止继续扫描旧文件。

早停必须发生在全部过滤条件之后，包括真实音频文件存在性检查。否则 `--has-audio --limit N` 可能少返回结果。

### 7.4 各命令行为

#### `voice list`

- 无任何选择条件且未传 `--all`：维持滚动最近 72 小时。
- `--all`：明确扫描全部已保存历史。
- `--app`、`--status`、`--language`、`--has-audio` 或显式时间范围均视为选择条件，不自动附加 72 小时。
- `--limit` 只是数量上限，不单独改变查询时间范围。

#### `voice search <keyword>`

- 只搜索最终上屏 `text`。
- 关键词本身是选择条件，因此未传时间范围时覆盖全部历史。
- 使用 Go 的 Unicode 大小写归一化做包含匹配，不搜索 App 名、窗口标题或 bundle ID。
- 与 `--app` 联用时先做廉价字段过滤，再检查音频文件。

#### `voice show <voice_id>`

- 从最新日期向旧日期扫描，找到 `voice_id` 后立即返回。
- 第一版没有 `voice_id → 文件/offset` 索引，因此最坏情况扫描全部历史。
- CLI 不接受数据库数字 `id` 作为新的稳定查询契约；如确需迁移期兼容，应显式命名为 legacy 行为并设置移除窗口。
- 未找到时保持 `YOOOCLAW_NOT_FOUND` 语义。

#### `voice +latest`

- 从最新日期文件开始。
- 返回全历史最新一条有效记录，不受默认 72 小时限制。
- 最新文件为空或只有坏行时继续扫描上一日期。

#### `voice +today`

- 只选择本地自然日对应文件，并以精确 `[today, tomorrow)` 时间边界复核。
- `--app` 使用完整 `app_id` 或 `app_name` 精确匹配。

#### `voice apps`

- 无时间范围且未传 `--all` 时，默认扫描滚动最近 7 天。
- `--all` 显式扫描全部历史。
- 带 `--from/--to` 时只统计范围内记录。
- 按 `app_id` 去重并返回 `app_id`、`app_name`、计数和最新时间。

#### `voice storage-path`

输出改为：

```json
{
  "ok": true,
  "path": "~/.yoooclaw/profiles/default/voice",
  "history": "~/.yoooclaw/profiles/default/voice/audio-jsonl",
  "audio": "~/.yoooclaw/profiles/default/voice/audio",
  "recordings_history": "~/.yoooclaw/profiles/default/voice/recordings-jsonl",
  "recordings": "~/.yoooclaw/profiles/default/voice/recordings",
  "format": "daily-jsonl"
}
```

移除 `database` 字段。即使历史目录尚未创建，`storage-path` 仍成功返回约定路径。

#### `voice stats`

删除该子命令，不提供兼容别名。

相关代码、类型、文档和 Skill 说明同步删除：

- `agent_voice_usage_daily_v1` / `usage_daily` 读取逻辑；
- `UsageDay` / `UsageTotal`；
- `ParseLocalDate`；
- `voice stats` Cobra 注册和测试；
- README、中文 README 与 Voice Skill 中的 stats 示例和语义。

删除后调用 `yoooclaw voice stats` 应由 Cobra 返回未知命令错误，不能继续从历史 JSONL 推算一个看似正确但语义不同的统计。

### 7.5 输入法会议录音查询

输入法会议录音属于“录音”语义，不放进短语音 `voice list/search`。现有顶层 `recording` 查询层应把它作为新的只读来源，并在输出中增加来源标识：

```json
{
  "source_type": "capture_app",
  "source_name": "YoooClaw Capture",
  "id": "531e3f9f-37c3-4c86-8896-914b263986b2",
  "title": "...",
  "recorded_at": "2026-08-20T14:21:05+08:00",
  "duration_ms": 134340,
  "status": "completed",
  "has_audio": true,
  "has_transcript": true,
  "has_summary": true,
  "missing_artifacts": []
}
```

两类来源的稳定映射为：

| 来源 | `source_type` | `source_name` | 数据位置 |
|---|---|---|---|
| 电脑端输入法会议录音 | `capture_app` | `YoooClaw Capture` | `<profile>/voice/recordings-jsonl` |
| 原有录音 | `smart_hardware` | `YoooClaw 智能硬件` | `<profile>/recordings` |

`source_type` 是脚本、过滤和 Agent 判断使用的稳定枚举；`source_name` 是面向用户的展示名称。原有录音中的 `clientLabel` 继续表示具体硬件/客户端实例，不能代替来源类型，也不能把 YoooClaw Capture 伪装成某个硬件 client。

混合列表中的每条记录都必须同时返回 `source_type` 和 `source_name`。Agent 在列出、比较或引用录音时必须带来源名称，例如：

```text
项目复盘（2026-08-20 14:21，YoooClaw Capture）
客户拜访（2026-08-20 10:05，YoooClaw 智能硬件）
```

建议查询接口：

```bash
yoooclaw recording list [--source all|capture_app|smart_hardware]
yoooclaw recording status <recording-id> [--source capture_app|smart_hardware]
yoooclaw recording +latest [--source all|capture_app|smart_hardware]
yoooclaw recording +today [--source all|capture_app|smart_hardware]
```

默认 `--source all`，使 Agent 查询“会议录音”时同时覆盖 YoooClaw Capture 和 YoooClaw 智能硬件。两类结果映射到统一发生时间后全局倒序；Capture 录音使用 `recorded_at`，硬件录音使用现有 effective `created_at`，并遵循相同的 `[from, to)` 时间边界。

如果同一个字符串 ID 同时存在于两个来源，`recording status <id>` 不得静默任选一条；未传 `--source` 时返回歧义错误并列出两个来源，调用方必须补充来源类型。

现有 `recording setup-asr`、`recording events` 等操作能力仍只属于 `smart_hardware` 来源，不作用于 `capture_app`。查询层统一不意味着两种生产端状态机合并。

读取录音内容时：

- 转写文件存在：按 `begin_time`、`end_time` 顺序读取 `transcripts[].sentences[]`。
- 摘要文件存在：直接读取 `summary.md`。
- 用户只问列表：不读取转写或摘要正文，只检查文件可用性。
- 用户请求的产物缺失：根据 `missing_artifacts` 明确报告当前缺失，不根据 `status` 猜测原因或进度。
- 同一录音 ID 在最终索引中意外出现多次：视为生产端数据异常，查询去重并给出诊断；不把它解释为状态更新流。

## 8. 产物路径与安全

短语音和会议录音的全部相对路径都以 `<profile>/voice` 为根，继续复用现有安全解析约束：

- `audio_rel_path`、`transcript_rel_path`、`summary_rel_path` 必须是相对路径。
- 拒绝绝对路径、`..` 逃逸和符号链接逃逸。
- 只有目标当前存在且为普通文件时，才返回对应 `has_*: true` 与绝对路径。
- 缺少音频不影响文本历史记录有效性。
- 会议录音索引声明的某个产物缺失，不影响该录音元数据和其他已存在产物被查询。
- 不根据录音 ID 自行拼出产物路径；读取始终以索引声明的相对路径为准，并验证各路径所在父目录与录音 ID 的一致性作为完整性诊断。

为了减少不必要的文件系统调用，只有记录已经通过时间、App、状态、语言和关键词过滤后，才检查音频文件。
会议录音列表只做路径安全与存在性检查，不读取转写和摘要正文。

## 9. 错误与容错

建议保留：

- `YOOOCLAW_VOICE_STORAGE_NOT_FOUND`：`audio-jsonl` 目录不存在。
- `YOOOCLAW_STORAGE_UNAVAILABLE`：目录或文件存在但无法读取。
- `YOOOCLAW_INVALID_ARGUMENT`：时间、ID、limit 等参数不合法。
- `YOOOCLAW_NOT_FOUND`：`voice show <voice_id>` 或 `recording status <recording-id>` 未命中。

YoooClaw Capture 的 `recordings-jsonl` 不存在时，统一录音查询把 `capture_app` 来源视为空，不影响 `smart_hardware` 结果；索引存在但不可读时返回存储不可用错误。单个已声明产物缺失不是命令级错误，而是通过 `has_*` 和 `missing_artifacts` 暴露。

删除只服务于 SQLite schema 的：

- `YOOOCLAW_VOICE_SCHEMA_UNSUPPORTED`。

坏行和查询统计建议由底层 reader 记录：

```go
type ScanStats struct {
    FilesScanned   int
    RowsScanned    int
    RowsMatched    int
    RowsSkipped    int
    StoppedAtLimit bool
}
```

第一版不必默认把扫描统计加入稳定 JSON 输出，但测试和 debug 日志应可观察；如果跳过了非末尾坏行，应通过现有日志/notice 机制让用户知道结果可能不完整。

## 10. 性能模型

| 查询 | 第一版复杂度 | 常见读取范围 |
|---|---|---|
| `+today` | O(当天记录) | 1 个文件 |
| 默认 `list` | O(最近 72 小时记录) | 3～4 个文件 |
| 带日期范围 | O(范围内记录) | 指定日期文件 |
| App/关键词 + limit | O(直到找到 N 条为止) | 从新到旧，可能提前停止 |
| `+latest` | O(最新有效日期记录) | 通常 1 个文件 |
| `voice show <voice_id>` | 最坏 O(全部短语音记录) | 找到即停止 |
| 默认 `apps` | O(最近 7 天记录) | 7～8 个日期文件 |
| `apps --all` | O(全部短语音记录) | 全历史 |
| `recording list --source capture_app` | O(所选日期 Capture 录音数) | 默认按日期文件扫描 |
| `recording status <id>`（输入法来源） | 最坏 O(全部会议录音索引) | 找到即停止 |

当前每条 JSON 大约是数百字节，数年、十万级记录仍适合本地顺序扫描。是否增加索引必须由 benchmark 和真实延迟驱动，不能仅因存在数百个日期文件就提前引入数据库复杂度。

建议加入至少两个 benchmark 数据集：

- 3 年 × 100 条/天，约 10.95 万条；
- 5 年 × 500 条/天，约 91.25 万条。

重点测量默认 72 小时、稀疏 App 的 `limit=20`、全历史关键词、旧 `voice_id` 查询、默认 7 天 `voice apps` 和 `voice apps --all`。会议录音另测数年索引的 list、latest 与旧 UUID 查询；列表测试不得读取转写正文。

如果未来全历史查询超过可接受预算，优先增加可重建派生索引，而不是改变 JSONL 的事实来源地位：

- 短语音 ID 索引：`voice_id → date file + byte offset`；
- 会议录音 ID 索引：`recording id → date file + byte offset`；
- App/日期摘要：每个日期或月份的真实 `app_id`/`app_name` 计数；
- 全文索引：仅用于关键词候选定位，最终结果仍回读 JSONL 验证。

索引删除后必须可以只靠 JSONL 完整重建。

## 11. 代码改造范围

### 11.1 `internal/voice`

- 保留并调整 `model.go` 中的 `Query`、`HistoryItem`、`AppSummary`；`HistoryItem` 对外 ID 改为字符串 `voice_id`，数据库数字 `id` 不进入 CLI 输出。
- 删除 `UsageDay`、`UsageTotal` 和 SQLite View/table 常量。
- 将 `repository.go` 改为 JSONL 文件 reader，不再持有 `*sql.DB`，也不再需要 `Close`。
- 新增日期文件枚举、单行解码、字段校验和扫描统计。
- App 过滤同时精确匹配原始 `app_id` 与 `app_name`，不维护别名表。
- 保留 `audio.go` 的路径安全处理。
- `time.go` 保留历史边界解析，删除仅为 stats 服务的 `ParseLocalDate`。

建议的 API 形态：

```go
type Repository struct {
    voiceDir   string
    historyDir string
}

func Open(voiceDir string) (*Repository, error)
func (r *Repository) List(ctx context.Context, opts Query) ([]HistoryItem, ScanStats, error)
func (r *Repository) Show(ctx context.Context, voiceID string) (*HistoryItem, ScanStats, error)
func (r *Repository) Apps(ctx context.Context, opts Query) ([]AppSummary, ScanStats, error)
```

`Open` 只验证目录和路径，不创建任何文件或目录。

### 11.2 输入法会议录音 reader

YoooClaw Capture 会议录音与现有智能硬件侧 `internal/recording.Storage` 的写入、下载和 ASR 状态机无关。建议新增独立的只读 reader（如 `internal/voicerecording`），职责仅包括：

- 枚举 `voice/recordings-jsonl` 每日文件；
- 解码并校验最终索引行；
- 按 UUID、日期和来源查询；
- 安全解析音频、转写和摘要路径；
- 基于真实文件计算 `has_audio`、`has_transcript`、`has_summary` 与 `missing_artifacts`；
- 在明确读取内容时解析 `transcript.json` 或读取 `summary.md`。

该 reader 不实现状态迁移、重试、ASR、文件修复或索引写入。

### 11.3 `internal/cli`

- 删除 `voice stats` 注册和 handler。
- 其他命令保持用户参数与主要输出字段。
- `storage-path` 改为输出 JSONL 与音频目录。
- `voice apps` 默认最近 7 天并返回 `app_id` 与 `app_name`；`--all` 才扫描全历史。
- 普通 App 查询只精确匹配完整 `app_id`/`app_name`，Agent 先用 `voice apps` 解析真实身份。
- `voice show` 接受 `voice_id` 字符串，不再把数据库整数作为稳定 ID。
- `recording list/status/+latest/+today` 合并 YoooClaw Capture 这个只读来源，并为两类录音输出 `source_type`、`source_name` 与产物可用性。
- `recording setup-asr/events` 保持智能硬件来源行为，不操作 YoooClaw Capture 录音。

### 11.4 依赖与错误码

完成测试迁移后，如果仓库没有其他生产代码使用 `modernc.org/sqlite`：

- 从 `go.mod` / `go.sum` 删除 SQLite 依赖；
- 删除测试中的 `database/sql` SQLite fixture；
- 删除 `CodeVoiceSchemaUnsupported`。

### 11.5 README 与 Skill

- 更新中英文 README 中 Voice 的描述和命令示例。
- 删除 `voice stats`。
- 将存储说明从 SQLite/WAL 改为每日 JSONL。
- 更新 `yoooclaw-context-query/references/voice-input.md`：App 条件先调用 `voice apps`，对照真实 `app_id`/`app_name` 后传入精确身份。
- 明确 `voice apps` 默认最近 7 天，历史范围可显式传 `--from/--to`，全历史需 `--all`。
- 将短语音输入与会议录音的来源边界写清：短语音走 `voice`，会议录音走统一 `recording` 查询。
- 更新 Recording Skill：内容请求以 `has_transcript` 和文件事实为准，不再要求所有来源都必须满足原 CLI 的 `status = transcribed`。
- 明确输入法会议录音的 `status` 是透传字段，Agent 不据此猜测缺失原因或处理进度。
- 要求 Agent 在混合录音结果中始终展示 `source_name`，并使用 `source_type` 做过滤和消歧。

## 12. 迁移与发布策略

1. 先实现短语音 JSONL reader、`voice_id` 对外身份、桌面 App 匹配和独立单元测试。
2. 将 CLI Voice 命令切换到 JSONL reader。
3. 删除 `voice stats` 与 SQLite-specific 代码、fixture 和依赖。
4. 实现输入法会议录音只读 reader，并接入统一 `recording` 查询。
5. 更新 README、Voice/Recording Skill 和帮助文本。
6. 用当前 profile 的真实 JSONL 和录音产物做只读验收，但自动化测试不能依赖用户目录。
7. 运行 Voice、Recording、CLI 及全仓测试。
8. 发布说明明确：新版本要求桌面语音输入 App 提供 `audio-jsonl`，不会回退读取旧 SQLite；输入法会议录音是新增只读来源。

旧的 `voice.sqlite3`、`voice.sqlite3-wal`、`voice.sqlite3-shm` 和备份文件不由 CLI 删除。是否清理由桌面 App 或后续明确的迁移工具负责。

## 13. 测试计划

至少覆盖：

### 文件与并发

- 多个日期文件按日期和记录时间正确倒序。
- 查询期间文件继续追加时只读取打开时快照。
- 无换行的最后半行被忽略。
- 中间坏行跳过，其他记录仍可查询。
- 非 JSONL 文件、目录和非法日期文件名被忽略。
- 文件在枚举后被删除不会使查询崩溃。

### 查询语义

- `[from, to)` 精确边界与跨时区边界。
- 默认最近 72 小时与 `--all`。
- `--limit` 早停且保持全局时间倒序。
- keyword 只匹配最终 `text`。
- status、language、has-audio 组合过滤。
- `+today` 和 `+latest`。
- `show` 命中与未命中。
- `voice show` 按 `voice_id` 查询，CLI 输出不暴露数据库数字 ID。
- `voice_id` 在没有音频时仍可查询；有音频时与文件 stem 一致。

### App 身份

- `voice apps` 默认范围严格为滚动最近 7 天；`--all` 扫描全历史。
- 每项同时返回真实 `app_id` 与 `app_name`。
- 相同 `app_id` 聚合计数并选择最近一次非空名称。
- 相同 `app_name`、不同 `app_id` 不合并。
- `{"app_id":"com.microsoft.VSCode","app_name":"Code"}` 能原样返回。
- `--app com.microsoft.VSCode` 和 `--app Code` 可命中该记录。
- `--app "VS Code"` 不作为别名命中，除非源数据的完整名称就是该值。
- App 匹配不使用别名、类别、模糊或子串扩展。

### 安全与兼容

- `app_id` 不出现在普通历史输出中，但在 `voice apps` 候选列表中显式返回。
- 数据库数字 `id` 不作为短语音 CLI 对外 ID。
- 音频绝对路径、`..` 和符号链接逃逸继续被拒绝。
- JSONL 存在时不访问 SQLite。
- 只有 SQLite、没有 `audio-jsonl` 时返回 storage not found，不做 fallback。
- `voice storage-path` 在目录不存在时仍返回约定路径。
- `voice stats` 不再出现在帮助和文档中。

### 输入法会议录音

- 每个最终索引 ID 只出现一次；重复 ID 被诊断而不解释为状态流。
- 按 `recorded_at` 正确处理今日、日期范围和最新录音。
- 录音 UUID 与产物父目录一致。
- `status` 原样透传，不参与 `has_*` 判断。
- 三种产物分别覆盖：未声明、真实存在、声明但缺失、路径逃逸、非普通文件。
- 缺失一个产物时，录音元数据与其他存在的产物仍可查询。
- 列表只检查文件事实，不读取转写和摘要正文。
- 内容请求按时间顺序拼接 `transcripts[].sentences[]`，并保留 `speaker_id`。
- ASR `task_id` 与录音 ID 不相等时仍可正常读取。
- `recording --source all` 合并 `capture_app` 和 `smart_hardware`，每条记录都带稳定类型和展示名称，并保持全局时间倒序。
- `recording --source capture_app|smart_hardware` 能准确过滤对应来源。
- 两个来源出现相同字符串 ID 时，未指定来源的单条查询返回歧义错误。
- `recording setup-asr/events` 不作用于 `capture_app` 来源。

## 14. 验收标准

- `rg "modernc.org/sqlite|agent_voice_history_v1|agent_voice_usage_daily_v1|usage_daily"` 不再命中 Voice 生产代码。
- 所有 Voice 历史命令只从 `audio-jsonl` 返回数据。
- 短语音 CLI 使用 `voice_id` 作为对外 ID；数据库数字 `id` 不再进入稳定输出或新查询契约。
- `voice apps` 默认最近 7 天，返回去重后的 `app_id`/`app_name`；`--all` 可显式扫描全历史。
- 普通 `--app` 查询只精确匹配完整桌面 App 名称或 ID，不存在内置别名扩展。
- 默认 72 小时、日期范围、排序、limit、音频安全和输出字段与既有契约一致，除文档明确列出的 breaking changes。
- `voice stats`、SQLite schema 错误码和 SQLite fallback 均已删除。
- 活跃写入造成的半行不会让查询失败或污染已完成记录。
- YoooClaw Capture 与 YoooClaw 智能硬件录音都能通过统一 `recording` 查询发现，每条结果都含正确的 `source_type` 和 `source_name`。
- Capture 音频、转写、摘要的可用性来自安全文件检查，而非 `status` 推断。
- 声明但缺失的会议录音产物通过 `has_* = false` 和 `missing_artifacts` 暴露给 Agent，不触发 CLI 修复或状态机逻辑。
- README、Skill、CLI help 与实现一致。

## 15. 明确的 breaking changes

1. `voice stats` 被删除。
2. `voice storage-path` 删除 `database`，新增 `history`、`audio`、`recordings_history`、`recordings`、`format`。
3. 安装了旧语音输入 App、只有 SQLite 而没有 `audio-jsonl` 的用户将无法使用 Voice 查询；CLI 不做 fallback。
4. App 筛选从“只精确匹配 `app_name`”扩展为“精确匹配 `app_name` 或 `app_id`”，不提供别名扩展。
5. `voice apps` 默认范围改为最近 7 天，按 `app_id` 去重并公开 `app_id`/`app_name`；全历史需要 `--all`。
6. 短语音对外 ID 从数据库整数切换为 `voice_id` 字符串；`voice show` 的新稳定参数也改为 `voice_id`。
7. `recording` 查询新增 `capture_app` 来源，并为两类录音新增 `source_type`、`source_name`；Capture 录音还增加 `has_summary`、`missing_artifacts` 等产物事实字段。

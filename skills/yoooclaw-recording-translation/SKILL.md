---
name: yoooclaw-recording-translation
description: 将文本文件和录音转录文件翻译为指定目标语言。支持两阶段翻译（提取术语表后翻译），保留时间戳标记，输出 Markdown sidecar 文件。当用户要求"翻译录音""翻译成[语言]""翻译文件""查看我的转录文件""用[语言]整理"时激活。基于录音时用 yoooclaw CLI 定位文件；yoooclaw 是 openclaw 手机录音插件的独立 CLI 形态，数据在 ~/.yoooclaw 下。
---

# yoooclaw 多语言翻译（Agent-Native）

> 命令名 `yoooclaw`，短别名 `yc` 完全等价。所有命令支持 `--format json|pretty|table|ndjson`，本 Skill 取结构化对象用 `--format json`。
> 录音数据在 `~/.yoooclaw` 下，查询纯读磁盘、不需要 daemon 在跑；定位/读写文件用你自己的文件工具，不要假设目录。

## 1. 初始化与定位

**若用户请求中包含明确文件路径**：

- 直接记录该路径，跳至 Step 3。

**若未提供文件路径**：

不要假设录音目录。调用以下命令获取存储路径：

```bash
yoooclaw recording storage-path --format json
# → {"ok":true,"path":"/abs/path/to/profiles/<profile>/recordings"}
```

后续所有文件操作以返回的 `path` 为根目录：

- 转写文件：来自 `recording status` 的 `transcriptFile`，即 `<path>/<transcriptFile>`。
- 翻译输出：`<path>/translation/<date>_<brief_summary>_translation_<lang>.md`

## 2. 意图解析与列表定位

> **若已在 Step 1 获得明确文件路径，跳过本节，直接进入 Step 3。**

### 2.1 确定目标语言与范围

从用户请求中识别以下信息：

**目标语言 (lang_code)**：
- 识别 `en` (英文), `ja` (日语), `ko` (韩语), `fr` (法语), `es` (西班牙语), `de` (德语) 等。
- **若未指定**：直接询问用户。

**目标录音范围**：
- 具体 ID/名称 -> 精确匹配。
- "刚才"、"最新" -> 取最近 1 条（可直接用 `yoooclaw recording +latest --format json`）。
- "最近 N 条" -> 取最近 N 条。
- "全部" -> 全量处理。
- **若未提及**：执行 2.2 展示列表。

### 2.2 获取列表与交互选择

```bash
yoooclaw recording list --status transcribed --format json
```

**过滤与排序**：
- 只保留 `status = "transcribed"` 且 `has_transcript = true` 的录音。
- 按 `created_at` 倒序。

**交互选择**：
若范围不明确，按序号展示过滤后的列表：
`1. [名称] ([时间], [时长])`
等待用户输入序号（支持 `1,3` 或 `1-3` 或 `all`）。

若用户表示找不到某条录音，说明该录音可能仍在转录中，告知用户稍后再试。
用户追问「到底卡在哪一步」时，用 `yoooclaw recording events --since 24h --format json`（或 `--id <recording_id>`）看状态事件流：`synced` 及之前是音频还在传，`transcribing` 才是正在转写，`transcribe_failed` 为转写失败。

## 3. 读取文件内容

**来自录音系统（recording list 流程）**：根据选定的 `recording_id` 获取具体路径：

```bash
yoooclaw recording status <recording_id> --format json
```

读取返回 `recording.transcriptFile` 对应的 `<path>/<transcriptFile>`。

若文件不存在或读取失败，告知用户：「转写文件尚未生成，对应的音频可能正在转写中，请稍后再试。」
- 单条录音：终止处理。
- 批量处理：跳过该条，继续处理其余录音。

**来自用户指定路径**：直接读取该文件。若文件不存在，告知用户该文件未找到，对应的音频可能仍在转写中，请稍后再试。

**注意**：必须完整保留 `**[关键点 MM:SS]**` 标记，用于后续翻译校对。

## 4. 两阶段翻译流程

### 4.1 第一阶段：上下文与术语提取

若文本 > 50 字，先提取背景。

**提示词要点**：
- 提取 `domain` (领域), `tone` (语气), `glossary` (术语表)。
- 术语表应包含人名、公司名、专业名词及缩写。
- 返回纯 JSON。

### 4.2 第二阶段：全文翻译

使用第一阶段的上下文和术语表进行翻译。

**强制规则**：
1. **严禁修改** `**[关键点 MM:SS]**` 标记。
2. **翻译元数据标签**（如 "录音名称"），但保留时间/日期数值。
3. **保持 Markdown 结构**（标题、引用块、分隔线）。
4. **术语一致性**：必须严格遵守提取的术语表。

## 5. 写入与反馈

### 5.1 写入翻译文件

用你自己的文件写入工具直接写出：

**来自录音系统**：写入 `<path>/translation/<date>_<brief_summary>_translation_<lang>.md`

**来自用户指定文件**：写入源文件同目录，如 `/path/to/<basename>_translation_<lang>.md`

若已存在，直接覆盖并在反馈中说明。

### 5.2 任务汇报

简洁汇报处理结果：
- 翻译语言及处理数量。
- 每条录音的领域、术语识别数及输出文件名。
- 若上下文提取失败，注明"已使用基础模式"。

## 边界处理

- **源语言=目标语言**：提示无需翻译并终止。
- **存储路径无效**：若用户未指定文件路径，告知录音功能不可用；若已指定路径则不受影响。
- **转写内容过短**：跳过术语提取，直接执行翻译。
- **路径缺失**：若 `transcriptFile` 为空，跳过并记录。

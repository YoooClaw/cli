---
name: yoooclaw-recording-entity-extraction
description: 从文本文件或录音转写中提取特定实体（人名、联系方式、机构、术语等）。支持自定义提取需求或通用提取，输出 sidecar JSON 文件。当用户要求"提取信息""找联系方式""有哪些人名""关键信息""从文件提取"时激活。基于录音时用 yoooclaw CLI 定位文件；yoooclaw 是 openclaw 手机录音插件的独立 CLI 形态，数据在 ~/.yoooclaw 下。
---

# yoooclaw 实体提取（Agent-Native）

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
- 实体输出：`<path>/entity/<date>_<brief_summary>_entities.json`

## 2. 意图解析与列表定位

> **若已在 Step 1 获得明确文件路径，跳过本节，直接进入 Step 3。**

### 2.1 确定范围与实体类型

从用户请求中识别以下信息：

**目标录音范围**：
- 具体 ID/名称 -> 精确匹配。
- "刚才"、"最新" -> 取最近 1 条（可直接用 `yoooclaw recording +latest --format json`）。
- "最近 N 条" -> 取最近 N 条。
- "全部" -> 全量处理。
- **若未提及**：执行 2.2 展示列表。

**实体类型**：
- 用户指定（如"手机号"、"品牌"）-> 仅提取指定类型。
- **若未指定**：执行通用提取（人名、联系方式、机构/公司、产品/品牌、日期时间、专业术语）。

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

保留所有文本内容及时间戳标记。

## 4. 实体提取流程 (LLM)

使用 LLM 从转写文本中提取实体。

**提取规则**：
1. **语义推断**：根据用户需求调整提取侧重点。
2. **去重**：同一实体多次出现仅保留一条，`context` 记录最典型的语境。
3. **语言一致性**：输出值应与转写文本语言一致。
4. **语境要求**：每个实体需配有 ≤30 字的出现背景。

## 5. 写入与反馈

### 5.1 写入 Sidecar 文件

用你自己的文件写入工具直接写出：

**来自录音系统**：写入 `<path>/entity/<date>_<brief_summary>_entities.json`

**来自用户指定文件**：同时写入以下两个位置：

1. **源文件同目录**：`/path/to/<basename>_entities.json`
2. **转录系统 entity 目录**：用 `yoooclaw recording storage-path --format json` 获取存储根路径，写入 `<storage_path>/entity/<basename>_entities.json`
   - 若该命令失败或返回路径无效，则跳过此步骤，仅保留源文件同目录的副本，无需报错。
   - 两份文件内容完全相同。

包含字段：`source_file`, `extracted_at`, `user_request`, `entity_types_extracted`, `entities`。

### 5.2 任务汇报

告知用户处理完成，并列出所有实际写入的文件完整路径（来自用户指定文件时可能为 1 或 2 个），供用户直接查阅。若处理了多条录音，逐条列出。

## 边界处理

- **无已转写录音**：提示用户并终止（仅适用于走 recording list 流程时）。
- **存储路径无效**：若用户未指定文件路径，告知录音功能不可用；若已指定路径则不受影响。
- **需求不明确**：默认进行全品类通用提取。
- **文件冲突**：若 sidecar 已存在，直接覆盖并在汇报中注明。

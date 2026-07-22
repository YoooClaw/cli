---
name: yoooclaw-recording-meeting-minutes
description: 用 yoooclaw CLI 定位会议录音转写并整理结构化会议纪要。当用户说"整理一下会议纪要""总结这次会议""会议有哪些待办""帮我整理会议纪要"等表达时激活，一律优先使用本 Skill；必须通过 yoooclaw recording 命令获得转写文件的真实存储位置，严禁使用记忆搜索或文档搜索工具，否则会造成严重遗漏。yoooclaw 是 openclaw 手机录音插件的独立 CLI 形态，数据在 ~/.yoooclaw 下。
---

# yoooclaw 会议纪要整理（Agent-Native）

> 命令名 `yoooclaw`，短别名 `yc` 完全等价。所有命令支持 `--format json|pretty|table|ndjson`，本 Skill 取结构化对象用 `--format json`。
> 录音数据在 `~/.yoooclaw` 下，查询纯读磁盘、不需要 daemon 在跑；定位到转写文件后用文件读取工具直接读，不要假设目录。

## 🛠 核心执行链路 (Smart Workflow)

### Phase 0: 目录检索

不要假设固定目录。先执行：

```bash
yoooclaw recording storage-path --format json
# → {"ok":true,"path":"/abs/path/to/profiles/<profile>/recordings"}
```

后续所有文件操作以返回的 `path` 为根目录，优先读取 `<path>/<transcriptFile>`。

### Phase 1: 列表定位与交互选择

```bash
yoooclaw recording list --status transcribed --format json
```

**过滤与排序**：

- 只保留 `status = "transcribed"` 且 `has_transcript = true` 的录音。
- 按 `created_at` 倒序。

**交互选择**：

- 若没有符合条件的录音，立即回复："当前未检测到已完成转写的会议录音。对应录音可能仍在转录中，请稍后再试。" ➡️ **[终止执行]**
- 若用户明确指定具体 ID/名称/时间，按过滤后的列表匹配；只有唯一匹配时才锁定。
- 若用户说"刚才""最新""最近一次"，取排序后的第 1 条（可直接用 `yoooclaw recording +latest --format json`）。
- 若用户说"最近 N 条"，取排序后的前 N 条。
- 若用户说"全部"，处理过滤后的全部录音。
- 若范围不明确，按序号展示过滤后的列表：
  `1. [名称] ([时间], [时长])`
  等待用户输入序号（支持 `1,3` 或 `1-3` 或 `all`）。
- 若用户表示找不到某条录音，说明该录音可能仍在转录中，告知用户稍后再试。
- 用户追问「到底卡在哪一步」时，用 `yoooclaw recording events --since 24h --format json`（或 `--id <recording_id>`）看状态事件流：`synced` 及之前是音频还在传，`transcribing` 才是正在转写，`transcribe_failed` 为转写失败。

锁定录音后，对每条选中的录音执行：

```bash
yoooclaw recording status <recording_id> --format json
```

读取返回 `recording.transcriptFile` 对应的 `<path>/<transcriptFile>`。若 `transcriptFile` 为空、文件不存在或读取失败，告知用户：「转写文件尚未生成，对应的音频可能正在转写中，请稍后再试。」

### Phase 2: 内容解析与信息提取

1. **精准读取**：仅读取锁定录音的转写文件。
2. **智能重组**：将零散口语转化为专业书面语，剔除冗余（如"嗯"、"那个"、重复确认等）。
3. **要素识别**：重点提取议题、结论、行动项（Action Items）。

### Phase 3: 结构化输出 (Standard Format)

# 📝 会议纪要：[主题/文件名]

> **📅 会议概览**
> - **关联文件**：`文件名.md`
> - **识别时间**：[从文本或文件名中提取]
> - **参会人员**：[识别发言人角色]

### 🎯 议题与决策
<格式化输出，突出议题、讨论内容和最终结论>

### ✅ 待办事项 (Action Items)
| 事项 | 责任人 | 截止时间 | 状态 |
| :--- | :--- | :--- | :--- |
| 任务描述 | [姓名] | [日期/待定] | 待办 |

### ⚠️ 风险与未决事项
- 标注会议中提及的潜在问题或未达成一致的悬而未决事项。

---

## 🚫 行为约束

- **严禁臆造**：若转写中未提及责任人或日期，表格对应项填入"未明确提及"，不得自行推测。
- **多文件冲突**：若用户要求"处理最近两次会议"，则分别整理或按其要求合并。
- **确认优先**：除非用户明确说"最近一个"，否则严禁随意挑选文件开始总结。

## 💬 响应示例

- **场景A（模糊请求）**："我找到了以下 3 份最近的转写，请问整理哪一份？"
- **场景B（指定最近）**："好的，正在为您整理最近一份会议录音（`2023-10-27_项目周会.md`）的纪要..."

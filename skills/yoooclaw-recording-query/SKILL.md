---
name: yoooclaw-recording-query
description: 用 yoooclaw CLI 查询本地长录音记录和转写内容。当用户说"有哪些录音""查一下录音""找最近的录音""这段录音说了什么""根据录音回答问题""查看录音摘要/转写""搜索录音内容"等需求时激活。必须通过 yoooclaw recording storage-path 与 recording list 定位录音，严禁假设录音目录。yoooclaw 是 openclaw 手机录音插件的独立 CLI 形态，数据在 ~/.yoooclaw 下，纯读磁盘、不需要 daemon 在跑。
---

# yoooclaw 长录音查询（Agent-Native）

`yoooclaw` CLI 自身就是工具表：所有命令都支持 `--format json|pretty|table|ndjson`。本 Skill 取结构化对象一律用 `--format json`。

> 命令名 `yoooclaw`，短别名 `yc` 完全等价。下文用 `yoooclaw`。
> 录音数据在 `~/.yoooclaw` 下，查询纯读磁盘、不需要 daemon 在跑。
> 定位到转写文件后，用你的文件读取工具直接读 `<path>/<transcriptFile>`，不要假设目录。
> 多 profile 时所有命令可加 `--profile <name>` 查指定 profile。

## 1. 初始化（不要假设录音目录）

```bash
yoooclaw recording storage-path --format json
# → {"ok":true,"path":"/abs/path/to/profiles/<profile>/recordings"}
```

后续所有文件操作以返回的 `path` 为根目录：

- 转写文件优先查找：`<path>/<transcriptFile>`（`transcriptFile` 来自 `recording status`）。
- AI 总结文件按需查找：`<path>/<summaryFile>`。
- 若命令失败或 `ok != true`，告知用户当前无法定位录音存储目录，终止查询。

## 2. 获取录音列表

```bash
yoooclaw recording list --format json
# 只看已转写的：yoooclaw recording list --status transcribed --format json
# 多设备时按 clientLabel 收窄：yoooclaw recording list --client work --format json（all 为全部）
```

返回示例：

```json
{
  "ok": true,
  "total": 2,
  "recordings": [
    {
      "id": "7E901C59-8B98-4AA3-9DE1-AE86E34A1A37",
      "name": "2026-05-11 10_31_43",
      "duration_sec": 66,
      "status": "transcribed",
      "file_size_bytes": 191283,
      "has_audio": true,
      "has_transcript": true,
      "created_at": "2026-05-11T10:31:43+08:00",
      "updated_at": "2026-05-11T02:33:19.805Z",
      "error": null
    }
  ]
}
```

`status` 取值是一条完整的传输 → 转写流水线，不是只有 `transcribed`：

`receiving` → `pending_oss_upload` → `uploading_oss` → `oss_uploaded` → `synced` → `transcribing` → `transcribed`，
另有两个失败态 `receiving_failed`（传输失败）与 `transcribe_failed`（转写失败）。
向用户解释「为什么还没有转写」时按实际状态说：`synced` 及之前是**音频还在传**，`transcribing` 才是**正在转写**。

处理规则：

- 若 `ok != true`，说明录音列表不可用，简短说明错误后终止。
- 若 `recordings` 为空，回复当前没有录音记录。
- 默认按 `created_at` 倒序展示或筛选。
- 查询转写内容时，只保留 `status = "transcribed"` 且 `has_transcript = true` 的录音。
- 若用户只是查询录音列表，可以展示所有录音，并标出未转写/失败状态。

## 3. 意图判断

识别用户目标：

- **列表查询**：如"有哪些录音"、"列一下录音"。展示录音列表，不读取全文。
- **范围查询**：如"最新一条"、"今天的"、"最近 N 条"、"全部"。按范围锁定录音。"最新一条"可直接用快捷命令 `yoooclaw recording +latest --format json`。
- **精确查询**：用户给出 ID、名称、时间。按 `id`、`name`、`created_at`、文件名片段精确或模糊匹配。
- **内容问答/搜索**：如"这段录音说了什么"、"有没有提到 X"、"总结一下最新录音"。锁定录音后读取转写文件回答。

若目标录音不明确且存在多条候选，展示候选列表并暂停等待用户选择：

```text
1. [name] ([created_at], [duration])
2. [name] ([created_at], [duration])
```

询问："请问要查询哪一条录音？可以回复序号、ID、名称，或 `all`。"

支持用户回复 `1,3`、`1-3`、`all`。

## 4. 定位文件

对每条选中的录音，调用：

```bash
yoooclaw recording status <recording_id> --format json
# 最新一条等价于：yoooclaw recording +latest --format json
```

返回的 `recording` 对象含 `transcriptFile` / `summaryFile` / `srtFile` / `audioFile` 等字段（均为相对 `storage-path` 的路径）。优先使用 `transcriptFile`，读取：

```text
<path>/<transcriptFile>
```

若 `transcriptFile` 为空或文件不存在，告知用户该录音的转写文件尚未生成或已被删除。

AI 总结文件按需读取：

- 当用户明确问"总结文件"、"AI 总结"、"摘要文件"时，读取 `<path>/<summaryFile>`。
- 普通内容问答优先读取 `transcriptFile` 转写文件。

## 5. 回答方式

### 列表查询

输出精简列表：

```text
1. 名称：...
   ID：...
   时间：...
   时长：...
   状态：...
   转写：有/无
```

不要输出大段 JSON，除非用户明确要求原始数据。

### 内容查询

读取锁定的 Markdown 转写文件后，根据用户问题回答：

- 优先基于转写原文，不要凭记忆或外部搜索补全。
- 可引用少量原文片段，但不要整段复制长转写。
- 若录音中未提到相关信息，明确说明"转写中未找到"。
- 多条录音查询时，按录音分组回答，并标明录音名称或 ID。

### 摘要查询

若读取的是 `summaryFile` 总结文件，说明来源是总结文件；若没有总结文件但读取了转写文件，则基于转写生成简短摘要，并说明"未找到已有总结文件，以下根据转写整理"。

## 6. 排查「转写迟迟不出来」

用户追问进度、或某条录音长期停在非 `transcribed` 状态时，查状态事件流（JSONL，纯读磁盘）：

```bash
yoooclaw recording events --since 1h --format json          # 最近 1 小时全部状态流转
yoooclaw recording events --id <recording_id> --format json # 只看某条录音
yoooclaw recording events --since 24h --limit 500 --format json
```

`--since` 接受 `10m` / `1h` / `24h` / `7d` 形式；返回 `{ok, path, total, events}`。
根据事件里最后一次流转判断卡在哪一环，再据此回复用户（还在传 / 正在转写 / 已失败及原因）。

若**所有**录音都从未进入 `transcribing`，多半是 ASR 没配置，提示用户运行：

```bash
yoooclaw recording setup-asr          # 交互式向导（api 或 local 模式）
```

本 Skill 只查询，不要替用户执行 `setup-asr`（它会写配置），告知命令即可。

## 7. 边界处理

- **未转写**：`status != "transcribed"` 或 `has_transcript != true` 时，按第 2 节的状态语义告知当前进展并建议稍后再试；需要细节时用 `recording events` 佐证。
- **转写失败**：`status = "transcribe_failed"`，展示 `error` 字段中的失败原因。
- **文件不存在**：说明索引存在但本地文件缺失，可能被删除或尚未同步完成。
- **大量录音**：若用户请求"全部"且数量很多，先展示数量并询问是否继续读取全文；列表查询不需要确认。
- **只读约束**：本 skill 只查询和读取文件，不写入、不删除、不重命名录音。
- **错误 schema**：命令失败统一输出 `{"ok":false,"error":{"code":"YOOOCLAW_...","message":"...","hint":"..."}}` 并以非零退出码结束，按 `message` / `hint` 处理。

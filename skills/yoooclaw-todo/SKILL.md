---
name: yoooclaw-todo
description: 使用 yoooclaw CLI 查询、创建、修改、完成、恢复和删除待办。普通待办和会议待办默认使用；用户明确指定飞书等其他平台作为操作目标时使用对应平台能力。
---

# 待办

使用 `yoooclaw --format json todo`（别名 `yc`），要求包含 todo 命令的 CLI 0.11.0 或后续版本。无需 daemon。凭据和环境沿用本机配置，不把 API Key 传入参数或回复。用户指定 profile 时使用全局 `--profile`。

## 选择操作

- `todo list`：按下表解析独立请求；用户明确条件优先。追问沿用上文筛选，只改变用户指定的部分，不重新套用独立请求默认值。按名称定位且没有日期时不限时间。查询边界左闭右开，有时间范围不包含无时间事项；根据 isLastPage 翻页，条件改变时从第一页开始。
- `todo get <todoId>`：详情；ID 必须来自已查询结果，始终作为字符串。
- `todo create`：结合当前本地时间和上下文推断缺失信息，不补问、不二次确认；不要把用户明确的过去时间改到未来。
- `todo update <todoId>`：只传修改字段，省略保留、null 清空。is-done=true 完成、false 恢复；目标或修改内容不清楚时先定位/澄清，不新建替代项。
- `todo delete <todoIds...> --confirmed`：先核对并让用户确认具体目标，再传 confirmed；取消则停止。

## 独立查询的四种默认范围

| 用户表达 | 时间范围 | 完成状态 |
| --- | --- | --- |
| 看看待办／今天有什么要做／我有什么没完成的／我有什么要做的 | 今天 | `--is-done false` |
| 明天有什么安排 | 明天 | `--is-done false` |
| 查一下已完成的 | 今天 | `--is-done true` |
| 查看全部待办 | 不限时间，不传时间边界 | `--is-done null` |

今天、明天均按用户当前时区计算当天零点到次日零点，显式传 `--start-time` 和 `--end-time` 的带偏移 ISO。明确“全部未完成”时不限时间、`--is-done false`。这些是 Agent 的自然语言规则；CLI 裸命令默认值仍为不限时间的未完成，不能用空参数代替“今天”。

## 时间与结构化输入

时间是事项的**开始时间**。定时传带用户时区偏移的 ISO，例如 `2026-09-11T15:00:00+08:00`；全天传 `2026-09-11` 并设置 isFullDay=true；无时间传 JSON null、isFullDay=false。CLI 负责 UTC 转换。切换定时/全天时同时传 dueAt 和 isFullDay。

所有命令支持 `--json-file <path>`；`--json-file -` 从 stdin 读 JSON 对象，不能和字段参数混用。通过 shell 传内容时使用带引号的 heredoc，防止用户文本被执行：

```sh
yoooclaw --format json todo create --json-file - <<'JSON'
{"title":"会议","dueAt":"2026-09-11T15:00:00+08:00","isFullDay":false}
JSON
```

日期仅为格式示例，执行时依据用户当前本地日期。无时间服务端若拒绝，报告实际错误，不擅自补时间。

## 结果与重试

同时检查退出码及 JSON 的 ok。仅成功后展示实际标题和开始时间，并提示“已添加到待办列表，可在 App 首页的待办卡片或列表中查看”。不新增跳转按钮、不重复成功提示。

create 返回 requestKey。网络结果未知时先核对，必要时只用 `--request-key <原键>` 和相同参数重试；不能重新生成键创建。duplicated=true 是原请求重放。CLI 已处理一次传输重试，不无限循环。

内容和完成状态可能分步成功：根据 contentUpdated、statusUpdated、unprocessedTodoIds 报告实际结果，仅处理失败部分。鉴权失败保留 code/message，不擅自归因为 App 登录失效；功能明确关闭时才提示去 App 开启。删除后不要引导查看已删除内容。

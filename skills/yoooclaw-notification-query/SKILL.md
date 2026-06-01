---
name: yoooclaw-notification-query
description: 用 yoooclaw CLI 直接、流式地查询手机通知原始数据。当用户说"看看最近的通知""谁找过我""总结今天的消息""某 App 有什么通知""昨天有什么消息"或任何通知查询/筛选/汇总诉求时，激活本 Skill，基于当前磁盘最新数据查询，不要依赖先前轮次或记忆。yoooclaw 是 openclaw 手机通知插件的独立 CLI 形态，数据在 ~/.yoooclaw 下，纯读磁盘、不需要 daemon 在跑。
---

# yoooclaw 通知查询（Agent-Native，流式）

`yoooclaw` CLI 自身就是工具表：所有命令都支持 `--format json|pretty|table|ndjson`。
**Agent 消费首选 `--format ndjson`**——每条通知一行 JSON、无包裹数组，便于流式逐条处理大批量结果。

> 命令名 `yoooclaw`，短别名 `yc` 完全等价。下文用 `yoooclaw`。

## 何时激活

- "最近收到什么消息 / 谁找过我 / 某某给我发过什么"
- "总结今天 / 昨天 / 最近一小时的通知"
- "微信 / 飞书 有什么通知"
- 任何按 时间 / 应用 / 发送人 / 关键词 筛选通知的诉求

## 先确认存储路径（不要假设目录）

```bash
yoooclaw notification storage-path --format json
# → {"ok":true,"path":"/abs/path/to/profiles/<profile>/notifications"}
```

## 查询（按需选参数）

```bash
# 今日全部（快捷命令，等价 search --from 今日00:00 --to 今日23:59）
yoooclaw notification +today --format ndjson

# 最近一小时
yoooclaw notification +recent --format ndjson

# 精确筛选：时间范围 + 应用 + 关键词
yoooclaw notification search \
  --from 2026-05-01T00:00:00+08:00 --to 2026-05-21T23:59:59+08:00 \
  --app 微信 --keyword 开会 --limit 200 --format ndjson

# 聚合摘要（topApps / topSenders / 最近样例），适合"帮我总结"
yoooclaw notification summary --top 10 --sample 30 --format json

# 维度统计（date|app|sender|hour|all）
yoooclaw notification stats --from 2026-05-14 --to 2026-05-21 --dim all --format json
```

- `--app` 支持中英文别名：`微信/wechat`、`飞书/feishu/lark`、`钉钉/dingtalk`、`企业微信/wecom` 等。
- `--from/--to` 用 ISO 8601 含时区（`2026-05-01T09:00:00+08:00`）。`stats` 的 `--from/--to` 用 `YYYY-MM-DD`。

## 流式处理 ndjson 的样板

```bash
yoooclaw notification search --app 微信 --format ndjson | while IFS= read -r line; do
  # 每行是一条 StoredNotification：{appName,appDisplayName,title,content,timestamp,senderName,conversationType,...}
  echo "$line" | jq -r '"\(.timestamp) \(.appDisplayName // .appName) | \(.senderName // .title): \(.content)"'
done
```

## 错误处理

所有命令失败都输出统一 schema 并以非零退出码结束：

```json
{ "ok": false, "error": { "code": "YOOOCLAW_...", "message": "...", "hint": "..." } }
```

- `YOOOCLAW_INVALID_ARGUMENT`：时间格式 / `--conversation-type`（只能 group|private）/ `--limit` 非正整数 → 按 `message` 修正参数。
- 通知目录尚不存在或当天无数据：`search` 返回 `[]`、`+today` 返回 `[]`，**不是错误**——直接据此回复"暂无通知"。
- 多 profile：加 `--profile <name>` 查指定 profile 的数据。

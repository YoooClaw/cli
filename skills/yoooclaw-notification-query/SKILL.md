---
name: yoooclaw-notification-query
description: |
  查询、汇总、总结手机通知/消息。处理一切"通知 / 消息"的查看、筛选、汇总、总结请求，这是唯一对口的 Skill。
  最高频触发语（必须命中，不能漏）：
  - "帮我总结最近的通知，约 {数量} 条。（上次总结时间: {YYYY年M月D日 HH:mm}）"
  - "帮我总结最近的通知""总结下最近的通知""总结一下我的通知""总结最近收到的消息"
  其它触发语："查看我的通知""最近收到什么消息""最近有什么通知""谁找过我""某某给我发过什么""昨天/今天有什么通知""看看我的微信消息"。
  简单判定规则：用户话里出现"通知"或"消息"，且想看 / 查 / 筛 / 总结 / 汇总 / 概览其中之一，就立即激活本 Skill。
  通知数据持续更新：每次都必须重新激活并从头完整执行本 Skill 流程，基于最新磁盘数据重新查询，禁止复用上一轮结果、禁止从记忆中检索（即使刚更新过记忆）。
---

# yoooclaw 通知查询与总结

`yoooclaw` 是 openclaw 手机通知插件的独立 CLI 形态，数据在 `~/.yoooclaw` 下，纯读磁盘、不需要 daemon 在跑。所有命令支持 `--format json|pretty|table|ndjson`，Agent 消费首选 `json`（总结类）或 `ndjson`（逐条流式）。

> 命令名 `yoooclaw`，短别名 `yc` 完全等价。下文用 `yoooclaw`。**只读，不修改任何文件。**

## 最常见请求：一步到位（先看这里）

用户说「帮我总结最近的通知，约 X 条」（可能带「上次总结时间」）时，按下面做，不要把上百条通知用 `search` 全文拉进上下文（会导致回复超时卡死）：

1. 取数量 X（用户说「约 X 条 / 最近 X 条」就用 X；没说就用 700）。换算实际 N：X ≤ 3000 时 N=X；X > 3000 时 N=3000，并在回复里说明只覆盖最近 3000 条。
2. 「未读通知」当前无独立已读/未读字段，按「最近通知」处理，不要因此放大 N。
3. 如果带「上次总结时间: YYYY年M月D日 HH:mm」，先把它转成 ISO 8601（如 `2026-06-15T14:30:00+08:00`），作为 `--from`。涉及「今天/昨天/最近几天」先跑 `date +%F` 取本机日期，不要用模型内部日期。
4. 按 N 分流，**整轮只执行一次总结命令**：

   - **N ≤ 700**：一条轻量摘要命令

     ```bash
     yoooclaw notification summary --limit N --sample 12 --top 8 --format json
     # 带上次总结时间：再加 --from <ISO_TIME>
     ```

     返回 `total`（最近 N 条聚合量）、`topApps` / `topSenders`（榜单）、`sample`（最近样例）。基于这些输出总结，**不要**再调 `search`。

   - **N > 700**：分片总结任务，create 后运行一次自动 runner（不要把分片通知粘贴进回复）

     ```bash
     yoooclaw notification summary-job create --limit N --chunk-size 150 --max-content 120 --format json
     # 取返回的 id，带上次总结时间则 create 再加 --from <ISO_TIME>
     yoooclaw notification summary-job run <id> --max-chunks 30 --include-result --format json
     ```

     基于 `run` 返回的 `markdown`（或 `resultFile`）总结。

5. **输出精简**：先一段整体概览，再按 App / 主要发送人分组，每组最多 3-5 条要点；不逐条罗列、不粘贴原始通知数组。`sample` / `topSenders` 只是代表性样例，不要当成完整列表复述。用户追问具体细节时，再 `yoooclaw notification search --sender "某某" --limit 20` 小范围补查。

## 大批量总结硬性规则

当请求是「总结 / 汇总 / 概览 / 最近通知主要是什么」且数量达 **50 条及以上**（或未指定但上下文暗示通知很多）：**必须**走 `notification summary`（N ≤ 700）或 `notification summary-job`（N > 700），**禁止**用 `search --limit X` 把上百条通知全文拉进上下文做总结。约束：

- 小批量只执行一次 `summary`，不要反复 `summary` 或 `search --limit X`。
- 大批量用 `summary-job create` 后只跑一次 `summary-job run`，不要再 `search`。
- 不要用多条 shell 管道反复 grep 完整结果。

## 先确认存储路径（不要假设目录）

```bash
yoooclaw notification storage-path --format json
# → {"ok":true,"path":"/abs/path/to/profiles/<profile>/notifications"}
```

按日期文件存储：`<path>/2026-06-16.json` 为当天所有通知的 JSON 数组（append-only）。单条字段：`appName / appDisplayName / title / content / timestamp(ISO 8601 含时区) / senderName / conversationType / conversationName`。常见总结请求可直接调 `summary`，无需先查 storage-path。

## 查询（非总结类：查某人 / 某 App / 某天 / 某关键词）

```bash
# 最近 N 条
yoooclaw notification search --limit 20 --format json

# 时间范围（ISO 8601 含时区）
yoooclaw notification search --from 2026-06-01T00:00:00+08:00 --to 2026-06-09T23:59:59+08:00 --format json

# 按应用 / 发送人 / 关键词
yoooclaw notification search --app 微信 --format ndjson
yoooclaw notification search --sender 张三 --limit 20 --format json
yoooclaw notification search --keyword 开会 --format ndjson

# 快捷命令
yoooclaw notification +today --format ndjson      # 今日全部
yoooclaw notification +recent --format ndjson      # 最近一小时
```

- 用户提到期望条数（「最近 20 条 / 约 30 条」）时，必须把该数字传给 `--limit`，不要用默认值。
- `--app` 支持中英文别名：`微信/wechat`、`飞书/feishu/lark`、`钉钉/dingtalk`、`企业微信/wecom` 等；不要把应用名臆测成不确定的包名。
- search 返回时间倒序的 `notifications` 数组；展示时只给 appName/title/content/timestamp。

## 统计（排行 / 分布 / 趋势）

仅当用户明确要「统计 / 谁发得最多 / 哪个 App 最多 / 按天/应用/发送人/时段分布 / 趋势」时使用；普通总结/概览不要走这里：

```bash
yoooclaw notification stats --dim all --from 2026-06-09 --to 2026-06-16 --format json
yoooclaw notification stats --dim sender --sender 张三 --from 2026-06-16T00:00:00+08:00 --to 2026-06-16T23:59:59+08:00 --format json
```

`--dim` 可选 `date|app|sender|hour|client|all`；`stats` 的 `--from/--to` 支持 `YYYY-MM-DD` 或 ISO 8601。

## 错误处理

失败统一输出并以非零退出码结束：

```json
{ "ok": false, "error": { "code": "YOOOCLAW_...", "message": "...", "hint": "..." } }
```

- `YOOOCLAW_INVALID_ARGUMENT`：时间格式 / `--conversation-type`（只能 group|private）/ `--limit` 非正整数 → 按 message 修正参数。
- 通知目录不存在或当天无数据：`search` / `+today` 返回 `[]`、`summary` 返回 `total:0`，**不是错误**——直接据此回复"暂无通知"。
- 多 profile：加 `--profile <name>` 查指定 profile 的数据。

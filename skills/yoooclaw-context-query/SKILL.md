---
name: yoooclaw-context-query
description: |
  The sole bundled Skill for querying fresh local data synchronized by yoooclaw: notifications/messages, recordings/transcripts, captured web pages/articles, images, or several of these sources together. Use for view, find, search, read, filter, count, answer, summarize, statistics, paths, and recent/latest requests. For the user's saved/bookmarked/viewed/read/opened articles or pages, use this Skill before browser automation or browser bookmark/history files.
  使用 yoooclaw 查询本地同步上下文的唯一 Skill。通知触发包括“总结最近的通知”“最近有什么通知”“谁找过我”“看看微信消息”“未读通知”；录音触发包括“有哪些录音”“找最近的录音”“根据录音回答问题”“查看录音摘要/转写”；网页触发包括“最近收藏的文章”“我刚才看的网页”“昨天保存了什么”“过去一周打开过的链接”；也处理已同步图片和“哪里提到过 X”等跨来源查询。灯效规则、录音翻译/会议纪要/思维导图/采访整理/实体提取不使用本 Skill。
---

# YoooClaw local context query

## Output language

Detect `replyLanguage` only from the user message that started the current request. Keep it unchanged for the run. Use it for all explanations, headings, labels, and summaries.

Do not infer reply language from previous messages, source content, command output, examples, or Skill text. Keep commands, paths, identifiers, JSON keys, timestamps, URLs, and quoted raw data unchanged.

## Command contract

- Use `yoooclaw`; `yc` is an equivalent short alias.
- Add `--format json` for structured objects and `--format ndjson` only for intentional row streaming.
- Add `--profile <name>` when the user selects a non-default profile.
- Query disk data on every invocation. Do not reuse an earlier answer or memory as current data.
- Treat queries as read-only. Do not modify indexes or synchronized source files.
- These local query commands do not require the daemon.

## Steps

### 1. Route the source

- Notifications/messages, senders, apps, recent/unread summaries: read `references/notifications.md`.
- Notification counts, rankings, distributions, or trends: also read `references/notification-statistics.md`.
- Recordings, transcripts, recording summaries, or questions about what was said: read `references/recordings.md`.
- Saved, viewed, read, opened, bookmarked, or captured pages/articles: read `references/web-pages.md`.
- Synchronized images, screenshots, metadata, or local image files: read `references/images.md`.
- Several sources or “local data/context” without one source: read every relevant reference and use the cross-source flow below.

Load only the references needed for the request.

### 2. Route the intent

- `list`: show available items without reading all full documents.
- `search`: locate items by keyword, metadata, sender, time, or source.
- `get`: resolve and read one identified item.
- `answer`: answer from selected source content.
- `summarize`: summarize a bounded source set using its source-specific safeguards.
- `statistics`: aggregate counts, rankings, distributions, or trends.

Resolve ambiguity before reading many full documents. If multiple material candidates remain, show a compact numbered list and ask the user to select.

### 3. Query fresh local data

Follow the selected reference and execute its `yoooclaw` commands during the current run.

For a cross-source query:

1. Extract a focused phrase plus any time or count constraint.
2. Query every relevant source for the full scope requested by the user:
   - notifications: focused `notification search`;
   - pages: `web search`;
   - recordings: locate indexed transcripts, then search only those files;
   - images: query metadata and inspect pixels only when requested.
3. Evaluate each source separately.
4. Merge relevant results grouped by source.

Do not apply a shared per-source result cap. Process large scopes in batches instead of truncating them or asking the user to narrow the request solely because of item count.

### 4. Answer with traceable sources

- No matches: state which local sources and filters were checked.
- Unsupported source: state that the adapter is unavailable.
- Current live Internet information: use an external web capability instead of presenting a captured copy as current.
- Include useful source labels: notification timestamp/app/sender, recording name/ID, page title/canonical URL, or image ID/path.

## Boundaries

- Persistent notification-triggered lighting uses `yoooclaw-lightrule-create`.
- Meeting minutes, translation, mind maps, interview restructuring, and entity extraction use `yoooclaw-recordings-process`.
- Relay, daemon, authentication, and ingest failures use `yoooclaw-tunnel-debug`.

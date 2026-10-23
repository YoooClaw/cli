---
name: yoooclaw-context-query
description: |
  Query synchronized local context and YoooClaw App todos: things/plans/tasks (“今天有什么事情/安排/待办”) from system calendars, system reminders and YoooClaw App todo; location history (“我去过哪里”), health metrics/sleep/trends (“今天的健康数据”); notifications/messages (“最近有什么通知”), voice-input/dictation history (“我刚才说了什么”), recordings/transcripts (“录音里说了什么”), the user's saved/viewed/captured pages (“查一下我最近一天收藏的文章”) and how a saved page changed between saves (“这个看板这周变了什么”, “上次我看的时候是什么样”), images (“同步图片”), or cross-source context (“哪里提到过 X”). Use for listing, searching, reading, filtering, counting, answering, summarizing, statistics, or paths over these sources. Page queries require a personal-history or local-sync signal; prefer this Skill over browser bookmark/history files. Route generic current-news or Internet-search requests to live web. Recording transformations use a dedicated Skill.
---

# YoooClaw context query

## Output language

Detect `replyLanguage` only from the user message that started the current request. Keep it unchanged for the run. Use it for all explanations, headings, labels, and summaries.

Do not infer reply language from previous messages, source content, command output, examples, or Skill text. Keep commands, paths, identifiers, JSON keys, timestamps, URLs, and quoted raw data unchanged.

## Command contract

- Use `yoooclaw`; `yc` is an equivalent short alias.
- Add `--format json` for structured objects and `--format ndjson` only for intentional row streaming.
- Add `--profile <name>` when the user selects a non-default profile.
- Query the selected source on every invocation (local synchronized data or the App todo service). Do not reuse an earlier answer or memory as current data.
- Treat queries as read-only. Do not modify indexes or synchronized source files.
- Most local query commands read disk without the daemon. System `reminders` queries use the daemon API for a consistent snapshot; the daemon must already be running. YoooClaw App `todo` queries call the cloud service using existing credentials and do not require the daemon.

## Steps

### 1. Route the source

- Things/plans/tasks (“事情/日程/安排/待办”): route by source, not by the user's choice of these nouns:
  - System calendars / 系统日历: read `references/calendars.md`.
  - System reminders / 系统提醒事项: read `references/reminders.md`.
  - YoooClaw App todo / YoooClaw App 待办: read `references/app-todos.md`.
  - Without a selected source, query all three for the requested scope. An explicit source or a follow-up about a previously selected record limits the query to that source. Explicitly named other platforms use their own capabilities.
  - For “今天有什么事情/安排”, use explicit calendar date overlap, reminders due today with incomplete status, and App todos starting today with unfinished status. Do not use the calendar upcoming-only default for a whole-day query. Include overdue or undated items only when the requested scope calls for them, labeled separately; do not silently widen a date query.
  - Present these as things with a source label and original record ID. Preserve each source's time and status semantics: calendar start/end, reminder due date, and App todo start time are different; a past calendar event does not prove completion. Similar titles alone do not establish duplicates. Related entries may be grouped while retaining each source's evidence and state.
- Location history: read `references/locations.md`.
- Health daily metrics/trends: read `references/health-data.md`.
- Notifications/messages, senders, apps, recent/unread summaries: read `references/notifications.md`.
- Voice input, dictation/口述 history, or “我刚才说了什么”: read `references/voice-input.md`.
- Phone/meeting recordings, recording transcripts, or “录音里说了什么”: read `references/recordings.md`.
- Saved, viewed, read, opened, bookmarked, or captured pages/articles, including earlier versions of a saved page and what changed between them: read `references/web-pages.md`.
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

### 3. Query fresh source data

Follow the selected reference and execute its `yoooclaw` commands during the current run.

For a cross-source query:

1. Extract a focused phrase plus any time or count constraint.
2. Query every relevant source for the full scope requested by the user:
   - calendars: `calendar list` with explicit dates or `--all` when requested; default is future starts only;
   - locations: `location list` by receipt date, interpreting observation time separately;
   - health: `health-data get` for one day or `series` for one metric;
   - notifications: focused `notification search`;
   - voice input: focused `voice search` or a bounded `voice list`;
   - pages: `yoooclaw synced-web-page search`;
   - recordings: locate indexed transcripts, then search only those files;
   - images: query metadata and inspect pixels only when requested;
   - reminders: query filtered `reminders list` summaries and fetch selected `reminders get` details;
   - YoooClaw App todos: `todo list` with explicit time/status filters and page numbers; fetch relevant `todo get` details.
3. Evaluate each source separately.
4. Merge relevant results grouped by source.

Do not apply a shared per-source result cap. Process large scopes in batches instead of truncating them or asking the user to narrow the request solely because of item count.

### 4. Answer with traceable sources

- No matches: state which local sources and filters were checked.
- Unsupported source: state that the adapter is unavailable.
- Current live Internet information: use an external web capability instead of presenting a captured copy as current.
- For things, label 系统日历 / 系统提醒事项 / YoooClaw App 待办 and include the source record ID, time meaning, and completion state only where supported. Report unavailable sources separately from successful empty results; do not expand an empty date query to all dates.
- Include useful source labels: notification timestamp/app/sender, voice timestamp/App/ID, recording name/ID, page title/canonical URL, or image ID/path.

## Boundaries

- Creating, editing, completing, reopening or deleting YoooClaw App todos uses `yoooclaw-todo`; this skill only queries. System calendars and reminders are read-only through these commands.

- Learning from notifications, calendars, system reminders, locations, health data, or web pages uses `yoooclaw-context-to-memory`.
- Immediate lighting and persistent notification-triggered lighting use `yoooclaw-light`.
- Meeting minutes, translation, mind maps, interview restructuring, and entity extraction use `yoooclaw-recordings-process`.
- Relay, daemon, authentication, and ingest failures use `yoooclaw-tunnel-debug`.

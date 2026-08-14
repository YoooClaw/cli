---
name: yoooclaw-context-query
description: |
  Query yoooclaw data already synchronized to this computer: notifications/messages (“最近有什么通知”), voice-input/dictation history (“我刚才说了什么”), recordings/transcripts (“录音里说了什么”), the user's saved/viewed/captured pages (“查一下我最近一天收藏的文章”), images (“同步图片”), or cross-source local context (“哪里提到过 X”). Use for listing, searching, reading, filtering, counting, answering, summarizing, statistics, or paths over these sources. Page queries require a personal-history or local-sync signal; prefer this Skill over browser bookmark/history files. Route generic current-news or Internet-search requests to live web. Recording transformations use a dedicated Skill.
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
- Voice input, dictation/口述 history, or “我刚才说了什么”: read `references/voice-input.md`.
- Phone/meeting recordings, recording transcripts, or “录音里说了什么”: read `references/recordings.md`.
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
   - voice input: focused `voice search` or a bounded `voice list`;
   - pages: `yoooclaw synced-web-page search`;
   - recordings: locate indexed transcripts, then search only those files;
   - images: query metadata and inspect pixels only when requested.
3. Evaluate each source separately.
4. Merge relevant results grouped by source.

Do not apply a shared per-source result cap. Process large scopes in batches instead of truncating them or asking the user to narrow the request solely because of item count.

### 4. Answer with traceable sources

- No matches: state which local sources and filters were checked.
- Unsupported source: state that the adapter is unavailable.
- Current live Internet information: use an external web capability instead of presenting a captured copy as current.
- Include useful source labels: notification timestamp/app/sender, voice timestamp/App/ID, recording name/ID, page title/canonical URL, or image ID/path.

## Boundaries

- Persistent notification-triggered lighting uses `yoooclaw-lightrule-create`.
- Meeting minutes, translation, mind maps, interview restructuring, and entity extraction use `yoooclaw-recordings-process`.
- Relay, daemon, authentication, and ingest failures use `yoooclaw-tunnel-debug`.

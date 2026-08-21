# Voice input history queries

Apply the parent `yoooclaw-context-query` rules.

## 1. Select this source

Use `voice` for short utterances recognized and inserted into another desktop App by the local
voice-input App. This source contains voice input, not keyboard typing or meeting recordings.

- “我刚才说了什么”, “今天口述了什么”, “我用语音输入过 X” → `voice`.
- “录音里说了什么”, meeting/phone recording, transcript, or recording summary → `recording`.
- With no recording signal, interpret “我说了什么” as voice-input history.

## 2. Query fresh local data

```bash
yoooclaw voice storage-path --format json
yoooclaw voice list [--from <ISO_OR_DATE>] [--to <ISO_OR_DATE>] [--all] \
  [--app <APP_ID_OR_EXACT_NAME>] [--status <STATUS>] [--language <LANGUAGE>] \
  [--has-audio] [--limit <N>] --format json
yoooclaw voice search <keyword> [--from <ISO_OR_DATE>] [--to <ISO_OR_DATE>] \
  [--app <APP_ID_OR_EXACT_NAME>] [--status <STATUS>] [--language <LANGUAGE>] \
  [--has-audio] [--limit <N>] --format json
yoooclaw voice show <voice_id> --format json
yoooclaw voice apps [--from <ISO_OR_DATE>] [--to <ISO_OR_DATE>] [--all] --format json
yoooclaw voice +latest --format json
yoooclaw voice +today [--app <APP_ID_OR_EXACT_NAME>] --format json
```

`--from` is inclusive and `--to` is exclusive. A date is local midnight. An unfiltered `voice list`
defaults to the rolling last 72 hours and returns `default_range_applied: true`, the exact `range`,
and a `notice`. State that scope when answering; never present it as all history. Use
`voice list --all` only when the user explicitly asks for all saved history. Any explicit selection
filter bypasses this default. `--limit` is only a caller-selected count cap and does not bypass the
default window.

The keyword in `voice search` is already a selection filter, so search covers all saved history
unless the user adds a time filter. With no time flags, `voice apps` defaults to the rolling last
seven days and reports `default_range_applied: true`; use `voice apps --all` only when an all-history
App inventory is explicitly needed. Neither command has a default count limit.

For “刚才”, compute exact RFC3339 boundaries from one hour before the current local system time to
the current time, then call `voice list --from <FROM> --to <TO>`. Do not use `voice +latest`: the
latest stored item could be old. If the one-hour range is empty, report that directly and never
substitute an earlier item. Use `+latest` only for an explicit unbounded “最新一条” request.

Use `voice +today` for today. Resolve yesterday or another calendar day as
`[local midnight, next local midnight)` and pass `--from/--to`.

`search` matches only final inserted `text`; it does not search App or window names. Use `show` for
one selected item. Its argument and returned `id` are the stable string `voice_id`, not a database
number. Treat `duration_ms` and `char_count` as App-defined facts; never recompute them.

## 3. Resolve an App condition

When the user restricts the App, first run `yoooclaw voice apps --format json`. For an explicitly
older query window, pass that same `--from/--to` range; otherwise keep the default recent-seven-day
inventory. Each candidate includes the observed `app_id` and `app_name`.

Match the user's product wording against both fields. Prefer passing the exact `app_id` to `--app`
when it identifies the product more clearly than the raw display name. For example, the observed
pair `{"app_id":"com.microsoft.VSCode","app_name":"Code"}` should be selected for VS Code by
passing `--app com.microsoft.VSCode`.

The CLI performs only case-insensitive exact matching on `app_id` or `app_name`; it has no built-in
alias or category expansion. If several candidates are plausible, show their ID/name pairs and ask
the user to choose. If none is plausible, report the searched App inventory scope rather than
inventing a value or silently dropping the condition.

“有没有说过飞书” searches text for `飞书`; “在飞书里说过什么” uses an App condition.

## 4. Interpret history and audio

- `voice list/search/apps` cover only history the user allowed the App to save. There is no
  `voice stats` command and history rows must not be presented as authoritative total usage.
- `has_audio` reflects whether the optional local audio file currently exists. Missing audio does
  not invalidate the text history.
- The reader uses daily JSONL under the path returned as `history`; it never falls back to SQLite.
- Query commands are local and read-only; they do not require the daemon or trigger upload,
  transcription, migration, or repair.

When no match exists, state the actual time, keyword, and App filters checked. When presenting a
match, include its timestamp, `app_name`, and string ID so the result remains traceable. `app_id` is
exposed by `voice apps` for identity resolution but remains absent from ordinary history rows.

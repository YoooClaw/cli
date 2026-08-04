# Voice input history queries

Apply the parent `yoooclaw-context-query` rules.

## 1. Select this source

Use `voice` for short utterances recognized and inserted into another App by the local voice-input
App. This source contains only voice input, not keyboard typing.

- “我刚才说了什么”, “今天口述了什么”, “我用语音输入过 X” → `voice`.
- “录音里说了什么”, meeting/phone recording, transcript, or recording summary → `recording`.
- With no recording signal, interpret “我说了什么” as voice-input history.

## 2. Query fresh local data

```bash
yoooclaw voice storage-path --format json
yoooclaw voice list [--from <ISO_OR_DATE>] [--to <ISO_OR_DATE>] [--all] \
  [--app <APP_NAME>] [--status <STATUS>] [--language <LANGUAGE>] \
  [--has-audio] [--limit <N>] --format json
yoooclaw voice search <keyword> [--from <ISO_OR_DATE>] [--to <ISO_OR_DATE>] \
  [--app <APP_NAME>] [--status <STATUS>] [--language <LANGUAGE>] \
  [--has-audio] [--limit <N>] --format json
yoooclaw voice show <id> --format json
yoooclaw voice +latest --format json
yoooclaw voice +today [--app <APP_NAME>] --format json
yoooclaw voice stats [--from <YYYY-MM-DD>] [--to <YYYY-MM-DD>] --format json
```

`--from` is inclusive and `--to` is exclusive. A date is local midnight. An unfiltered `voice list`
defaults to the rolling last 72 hours and returns `default_range_applied: true`, the exact `range`,
and a `notice`. State that scope when answering; never present it as all history. Use
`voice list --all` only when the user explicitly asks for all saved history. Any explicit selection
filter bypasses this default. `--limit` is only a caller-selected count cap and does not bypass the
default window.

The keyword in `voice search` is already a selection filter, so search covers all saved history
unless the user adds a time filter. `voice apps` covers all candidates unless given `--from/--to`.
`stats` covers all usage days unless given date bounds. None of these commands has a default count
limit.

For “刚才”, compute exact RFC3339 boundaries from one hour before the current local system time to
the current time, then call `voice list --from <FROM> --to <TO>`. Do not use `voice +latest`: the
latest stored item could be old. If the one-hour range is empty, report that directly and never
substitute an earlier item. Use `+latest` only for an explicit unbounded “最新一条” request.

Use `voice +today` for today. Resolve yesterday or another calendar day as
`[local midnight, next local midnight)` and pass `--from/--to`.

`search` matches only final inserted `text`; it does not search App or window names. Use `show` for
one selected item. Treat `duration_ms` and `char_count` as App-defined facts; never recompute them
from timestamps or text length.

## 3. Resolve an App condition

Users name Apps with visible names, never `app_id`. When the user explicitly restricts the App:

1. Run `yoooclaw voice apps --format json`; include the same `--from/--to` when a time range exists.
2. Match the user's wording against the returned `app_name` candidates.
3. Only when one candidate is clearly intended, pass its exact returned `app_name` to `--app`.
4. If several candidates remain plausible, show them and ask the user to choose.
5. If no candidate exists, report that; do not invent a name or silently drop the App condition.

“有没有说过飞书” searches text for `飞书`; “在飞书里说过什么” uses an App condition. Never
expose or pass `app_id`.

## 4. Interpret history, usage, and audio

- `voice list/search/apps` cover only history the user allowed the App to save.
- `voice stats` reads authoritative daily usage, including successful inputs made while history
  saving was disabled. Never derive total usage from history rows.
- `has_audio` reflects whether the optional local audio file currently exists. Missing audio does
  not invalidate the text history.
- Query commands are local and read-only; they neither require daemon nor trigger upload,
  transcription, database migration, or repair.

When no match exists, state the actual time, keyword, and App filters checked. When presenting a
match, include its timestamp, `app_name`, and ID so the result remains traceable.

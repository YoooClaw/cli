# Recording source selection

Apply the parent `yoooclaw-recordings-process` language, profile, command, and integrity rules. This branch produces `selectedSources`.

## Explicit input

When the artifact accepts provided text or a readable file:

- lock only that content or file;
- preserve its path and literal timestamp markers;
- report an unreadable single input and stop;
- in an explicit batch, skip unreadable inputs and report them.

Meeting-minutes and interview branches accept transcript/text files. Translation, mind-map, and entity branches also accept directly provided text.

## Recording-system input

### 1. Locate and list

```bash
yoooclaw recording storage-path --format json
yoooclaw recording list --status transcribed \
  [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>] --format json
yoooclaw recording +today --status transcribed --format json
```

Use the returned root as `<storage-path>`. Keep entries with `status = "transcribed"` and `has_transcript = true`, sorted by `created_at` descending.

If no eligible recording exists, report that transcripts are not currently available and stop.

### 2. Select

- Explicit ID, name, time, or filename fragment: require a unique match.
- Today/今天: resolve the current local system date and use `recording +today --status transcribed`.
- Yesterday/昨天 or an explicit calendar day: resolve `[local midnight, next local midnight)` and use `recording list --from <ISO_FROM> --to <ISO_TO> --status transcribed`.
- Rolling periods: compute exact ISO boundaries from the current local system time and use `--from/--to`.
- Latest/most recent/刚才/最新/最近一次: select the first entry; `recording +latest` may be used.
- Most recent N/最近 N 条: select the first N entries.
- All/全部: select every eligible entry.
- Ambiguous: show `1. [name] ([created_at], [duration])` and ask for an index, ID, `1,3`, `1-3`, or `all`.

Never fall back from an empty date range to latest. Report that no eligible recording exists in the requested range.

### 3. Resolve transcripts

For each selection:

```bash
yoooclaw recording status <recording_id> --format json
```

Read `<storage-path>/<transcriptFile>`.

An empty, missing, or unreadable `transcriptFile` is unavailable: stop for one selection; skip and report it in a batch.

Store for each `selectedSources` item:

- recording ID and name;
- creation time, duration, and status;
- absolute transcript path and content;
- source kind (`recording`, `explicit-file`, or `provided-text`).

When the user asks why a recording is unavailable, diagnose through `yoooclaw recording events --id <recording_id> --format json`; do not call setup commands or modify configuration.

## Integrity

Use only `selectedSources` for artifacts. Preserve source files, recording indexes, paths, identifiers, timestamps, and literal markers.

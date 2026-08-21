# Recording source selection

Apply the parent `yoooclaw-recordings-process` language, profile, command, and integrity rules. This
branch produces `selectedSources`.

## Explicit input

When the artifact accepts provided text or a readable file:

- lock only that content or file;
- preserve its path and literal timestamp markers;
- report an unreadable single input and stop;
- in an explicit batch, skip unreadable inputs and report them.

Meeting-minutes and interview branches accept transcript/text files. Translation, mind-map, and
entity branches also accept directly provided text.

## Recording-system input

### 1. Locate and list

```bash
yoooclaw recording storage-path --format json
yoooclaw recording list [--source all|capture_app|smart_hardware] \
  [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>] --format json
yoooclaw recording +today [--source all|capture_app|smart_hardware] --format json
```

The default `all` covers both `YoooClaw Capture` and `YoooClaw 智能硬件`. Keep entries with
`has_transcript = true`; do not filter on one shared status because Capture and hardware use
different status vocabularies. Mixed results are already sorted by actual occurrence time
descending. Preserve `source_type` and `source_name` with every candidate.

If no eligible recording exists, report that transcripts are not currently available and stop.

### 2. Select

- Explicit ID, name, time, or filename fragment: require a unique match.
- Today/今天: use `recording +today`, then keep `has_transcript = true` entries.
- Yesterday/昨天 or an explicit day: resolve `[local midnight, next local midnight)` and use
  `recording list --from <ISO_FROM> --to <ISO_TO>`.
- Rolling periods: compute exact ISO boundaries from the current local system time.
- Latest/most recent/刚才/最新/最近一次: select the first eligible entry; `recording +latest` may
  locate the newest item, but verify it has a transcript before processing.
- Most recent N/最近 N 条: select the first N eligible entries. All/全部: select every eligible entry.
- Ambiguous: show `1. [name] ([created_at], [duration], [source_name])` and ask for a selection.

Never fall back from an empty date range to latest. Report that no eligible recording exists in the
requested range.

### 3. Resolve transcripts

For each selection, preserve and pass its source:

```bash
yoooclaw recording status <recording_id> --source <source_type> --format json
```

- `capture_app`: read the returned absolute `transcript_path`. Flatten
  `transcripts[].sentences[]` by `begin_time`, `end_time`, preserving `speaker_id`. Do not treat
  transcript `task_id` as the recording ID.
- `smart_hardware`: read `<smart_hardware.path>/<transcriptFile>`, using the smart-hardware root
  returned by `recording storage-path`.

An item with `has_transcript = false`, a path in `missing_artifacts`, or an unreadable transcript is
unavailable: stop for one selection; skip and report it in a batch. Do not use producer `status` to
override these file facts.

Store for each `selectedSources` item:

- recording ID, name, source type, and source name;
- occurrence time, duration, and status;
- absolute transcript path and content;
- source kind (`recording`, `explicit-file`, or `provided-text`).

When the user asks why a smart-hardware recording is unavailable, diagnose with
`yoooclaw recording events --id <recording_id> --format json`. Capture has no hardware event stream;
report its `missing_artifacts` without calling setup commands or modifying configuration.

## Integrity

Use only `selectedSources` for artifacts. Preserve source files, recording indexes, paths,
identifiers, timestamps, speaker IDs, and literal markers.

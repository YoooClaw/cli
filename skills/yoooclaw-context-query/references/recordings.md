# Recording queries

Apply the parent `yoooclaw-context-query` rules.

## 1. Locate and list

```bash
yoooclaw recording storage-path --format json
yoooclaw recording list --format json
# Optional filters:
yoooclaw recording list --status transcribed --client <label> \
  [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>] --format json
yoooclaw recording +today --format json
```

Use the returned storage `path`. Recording lists are sorted by effective `created_at` descending. `--from` is inclusive and `--to` is exclusive. List requests may include every status; content requests use entries with `status = "transcribed"` and `has_transcript = true`.

## 2. Resolve scope

- Explicit ID, name, time, or filename fragment: require a unique match.
- Today/今天: resolve the current local system date and use `yoooclaw recording +today --format json`. Add `--status transcribed` for transcript/content requests.
- Yesterday/昨天 or an explicit calendar day: resolve `[local midnight, next local midnight)` and run `recording list --from <ISO_FROM> --to <ISO_TO>`.
- Rolling periods such as the last 24 hours or last N days: compute exact ISO boundaries ending at the current local system time and use `--from/--to`.
- Latest/most recent: use `yoooclaw recording +latest --format json`.
- Most recent N: select the first N sorted entries.
- All: use all eligible entries.
- Ambiguous: show `1. [name] ([created_at], [duration])` and ask for an index, ID, name, `1,3`, `1-3`, or `all`.

Never substitute latest/most recent for today, yesterday, or another explicit date range. If the requested range has no recordings, report that it is empty; do not fall back to an older recording.

A metadata-only list does not require transcript reads.

## 3. Resolve files

For each selected recording:

```bash
yoooclaw recording status <recording_id> --format json
```

Resolve paths relative to the storage root:

- transcript: `<path>/<transcriptFile>`;
- existing summary: `<path>/<summaryFile>`;
- subtitles/audio when explicitly requested: `<path>/<srtFile>` / `<path>/<audioFile>`.

Use transcript text for content search, Q&A, or a newly generated summary. Read `summaryFile` only when the user asks for the stored/AI summary. If no stored summary exists but a transcript does, say that the answer or summary was generated from the transcript.

## 4. Status and failure diagnosis

Typical progression:

`receiving → pending_oss_upload → uploading_oss → oss_uploaded → synced → transcribing → transcribed`

Failure states include `receiving_failed` and `transcribe_failed`.

When progress detail is requested:

```bash
yoooclaw recording events --id <recording_id> --format json
yoooclaw recording events --since 24h --limit 500 --format json
```

Do not describe `synced` as currently transcribing; only `transcribing` means ASR is running.

## 5. Answer

- Lists: compact metadata, status, and transcript availability.
- Q&A/search: answer only from selected transcript text and use short quotations.
- No mention: state that the selected transcript does not contain the requested information.
- Multiple recordings: group by recording name or ID.
- Missing transcript: report the real status/error; do not guess.

This branch is read-only. Recording transformations use `yoooclaw-recordings-process`.

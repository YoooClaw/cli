# Recording queries

Apply the parent `yoooclaw-context-query` rules.

## 1. Locate and list both sources

```bash
yoooclaw recording storage-path --format json
yoooclaw recording list [--source all|capture_app|smart_hardware] \
  [--status <STATUS>] [--client <label>] \
  [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>] --format json
yoooclaw recording +today [--source all|capture_app|smart_hardware] --format json
yoooclaw recording +latest [--source all|capture_app|smart_hardware] --format json
```

The default source is `all`. Every item identifies its origin:

- `capture_app` / `YoooClaw Capture`: meetings recorded by the desktop voice-input App.
- `smart_hardware` / `YoooClaw 智能硬件`: recordings synchronized from YoooClaw hardware.

Mixed lists are sorted globally by actual occurrence time descending. `--from` is inclusive and
`--to` is exclusive. `--client` identifies a hardware instance and therefore excludes Capture when
set to a specific value. Include `source_name` whenever presenting a recording to the user.

## 2. Resolve scope

- Explicit ID, name, time, or filename fragment: require a unique match.
- Today/今天: use `yoooclaw recording +today --format json`.
- Yesterday/昨天 or an explicit day: resolve `[local midnight, next local midnight)` and run
  `recording list --from <ISO_FROM> --to <ISO_TO>`.
- Rolling periods: compute exact ISO boundaries ending at the current local system time.
- Latest/most recent: use `yoooclaw recording +latest --format json`.
- Most recent N: select the first N sorted entries. All: use every eligible entry.
- Ambiguous: show `1. [name] ([created_at], [duration], [source_name])` and ask for a selection.

Never substitute latest for today, yesterday, or another explicit range. If the range is empty,
report that directly. A metadata-only list does not require transcript or summary reads.

## 3. Resolve one recording and its files

Always carry `source_type` from the selected list item:

```bash
yoooclaw recording status <recording_id> --source <source_type> --format json
```

Passing the source avoids ambiguity if both producers happen to use the same string ID. File rules
depend on the returned source:

- `capture_app`: use the safe absolute `transcript_path`, `summary_path`, or `audio_path`. Each path
  is present only when its declared artifact currently exists as a regular file. Read a Capture
  transcript by flattening `transcripts[].sentences[]` in `begin_time`, `end_time` order and retain
  `speaker_id`; its ASR `task_id` is not the recording ID.
- `smart_hardware`: use the source root in `recording storage-path` and resolve the existing
  `transcriptFile`, `summaryFile`, `srtFile`, or `audioFile` relative to that root.

Use transcript content for Q&A, search, or a newly generated summary. Read a stored summary only
when the user asks for it. If no stored summary exists but a transcript does, state that the answer
or summary was generated from the transcript.

`has_audio`, `has_transcript`, and `has_summary` are the availability facts. For Capture,
`missing_artifacts` names paths declared by the index but currently unavailable. Do not infer file
availability from `status`, and do not repair missing files.

## 4. Status and failure diagnosis

Hardware status progression may include:

`receiving → pending_oss_upload → uploading_oss → oss_uploaded → synced → transcribing → transcribed`

Hardware failure states include `receiving_failed` and `transcribe_failed`. Only hardware exposes
the daemon state event flow:

```bash
yoooclaw recording events --id <recording_id> --format json
yoooclaw recording events --since 24h --limit 500 --format json
```

YoooClaw Capture's `status` is producer metadata only. If a Capture artifact is missing, report the
corresponding `has_*` and `missing_artifacts` facts rather than applying the hardware state model.

## 5. Answer

- Lists: compact metadata, `source_name`, status, and artifact availability.
- Q&A/search: answer only from selected transcript text and use short quotations.
- No mention: state that the selected transcript does not contain the requested information.
- Multiple recordings: group by recording name/ID and source.
- Missing transcript: report the file facts and any source-specific error; do not guess.

This branch is read-only. Recording transformations use `yoooclaw-recordings-process`.

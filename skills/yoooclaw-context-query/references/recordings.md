# Recording queries

Apply the parent `yoooclaw-context-query` rules.

## 1. Locate and list both sources

```bash
yoooclaw recording storage-path --format json
yoooclaw recording list [--source all|mobile_app|capture_app] \
  [--status <STATUS>] [--client <label>] \
  [--from <ISO_TIME_OR_DATE>] [--to <ISO_TIME_OR_DATE>] --format json
yoooclaw recording +today [--source all|mobile_app|capture_app] --format json
yoooclaw recording +latest [--source all|mobile_app|capture_app] --format json
```

The default source is `all`. Every item identifies its origin:

- `capture_app` / `YoooClaw Capture`: meetings recorded by the desktop voice-input App.
- `mobile_app` / `YoooClaw 智能硬件`: recordings synchronized from the YoooClaw hardware that
  the App connects to. When the user says "YoooClaw 硬件", "智能硬件", "硬件录音", or names a
  hardware device, still use `mobile_app`. `smart_hardware` is only accepted as a legacy
  `--source` input alias; `source_type` in output is always `mobile_app`.

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
- `mobile_app`: use the source root in `recording storage-path` and resolve the existing
  `transcriptFile`, `summaryFile`, `srtFile`, or `audioFile` relative to that root.

For a stored hardware summary, prefer `summaryFile` returned by status. If status omits the path,
read the hardware index reported by `recording storage-path` (or `<hardware-root>/index.json` on
older versions), find the exact recording ID in `recordings[]`, and use that entry's `summaryFile`.
Both `summaries/...` and migrated `files/transfer-...` paths are valid; do not derive a filename
from the title or ID. Resolve indexed paths within the hardware storage root and read them as data.
Only when neither status nor the index declares a summary path may you search `<hardware-root>/summaries/`
for a legacy filename ending in `_<id>.md` or named `<id>.md`; require a unique match.
If a declared path is missing or unreadable, report the summary file as unavailable rather than
searching for a substitute or saying no summary was generated. If the index cannot be read or
parsed, report that lookup failure rather than treating it as an absent path.

Use transcript content for Q&A, search, or a newly generated summary. Read a stored summary only
when the user asks for it. If no stored summary exists but a transcript does, state that the answer
or summary was generated from the transcript.

`has_audio`, `has_transcript`, and `has_summary` are the availability facts. For Capture,
`missing_artifacts` names paths declared by the index but currently unavailable. Do not infer file
availability from `status`, and do not repair missing files.

## 4. Status and failure diagnosis

Hardware status progression may include:

`receiving → pending_oss_upload → uploading_oss → oss_uploaded → synced → transcribing → transcribed`

Hardware failure states include `receiving_failed` and `transcribe_failed`. `recording events`
merges the `mobile_app` and `capture_app` event streams, sorted by event time; each event carries
`sourceType`, so use that field to attribute diagnosis to its source:

```bash
yoooclaw recording events --id <recording_id> --format json
yoooclaw recording events --since 24h --limit 500 --format json
```

YoooClaw Capture's `status` is producer metadata only. If a Capture artifact is missing, report the
corresponding `has_*` and `missing_artifacts` facts rather than applying the hardware state model,
even when an event mentions the same recording.

## 5. Answer

- Lists: compact metadata, `source_name`, status, and artifact availability.
- Q&A/search: answer only from selected transcript text and use short quotations.
- No mention: state that the selected transcript does not contain the requested information.
- Multiple recordings: group by recording name/ID and source.
- Missing transcript: report the file facts and any source-specific error; do not guess.

## 6. Deletion

Deleting is the only recording mutation available to this Skill, and it must go through the CLI
so the daemon updates `recordings/index.json` and the files together:

```bash
yoooclaw recording delete <recording_id> --source <source_type> --yes --format json
yoooclaw recording delete <recording_id> --source mobile_app --local-only --yes --format json
```

- Default: delete the index entry and all local artifacts (`--source` defaults to `mobile_app`).
- `--local-only`: clear local artifacts but keep the entry pending re-download. Use only when the
  user asked to clean up local files rather than delete the recording.
- `--yes` is required in non-interactive runs; without it the command asks for confirmation.
- Confirm the target first with `recording status`/`recording list`, and require a unique match.
  Never delete several recordings because a filter matched; list them and let the user choose.

Never edit `recordings/index.json` directly and never `rm`/trash files under the recordings root
(`audio/`, `transcripts/`, `transcript-data/`, `summaries/`). The daemon keeps the index in memory
and rewrites the whole file on its next change, so a direct edit appears to work and then reverts.
Only the command above reaches the daemon's write path.

## Boundary

Query steps in this branch are read-only. Recording transformations use
`yoooclaw-recordings-process`.

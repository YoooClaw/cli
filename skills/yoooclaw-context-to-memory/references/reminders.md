# System reminders memory

Use `yoooclaw sync next --source reminders --format json`, then the parent skill's
exact-token `sync commit --source reminders --token <token>` after memory read-back.
The daemon must already be running. Do not read or edit reminder partitions directly.
Each batch contains at most 20 changed records (possibly fewer for the byte budget).
Process at most three nonempty batches for this source per run, independently of
other sources. Default scope is all unconsumed current records; no due date is
required. Unfinished records are returned before completed records.

An explicit `--date` or `--from-date` / `--to-date` selects the source-local day of
`modifiedAt`, falling back to `createdAt`, then source `lastCollectedAt` if both
are unavailable. This is a change/observation scope, never an execution date or
complete history. State the fallback when interpreting unknown system timestamps.
Do not widen an explicit empty date scope. `--all` explicitly selects every pending
current record. Ordinary `reminders list/get` does not advance consumption.

- Identity is `id` (the source-scoped recordId); `version` hashes the full record.
  `previousData` / `previousVersion` are the last acknowledged version, if any.
  Unchanged records are skipped even when the source is synchronized again.
- Read all records in the frozen batch. Keep a new unfinished reminder as a task
  only when useful to memory. Missing `due` means no supplied deadline, not today,
  not no reminder alarm. Creation time does not schedule execution.
- Update the same sourced fact for title, notes, list, due date or priority changes.
  `isCompleted` determines completion; only `completedAt` supplies completion time.
  A true-to-false transition reopens the source reminder. Do not reopen a separate
  user-closed Harness task without authorization.
- Historical completed reminders are not new actionable tasks. Preserve only
  useful outcomes or durable facts; fully evaluated no-write batches may be committed.
- A record absent from the latest mirror is not proof of deletion, cancellation
  or completion. This protocol emits present-record revisions, not disappearance
  events; do not claim it synchronizes deletions into memory.
- A pending batch stays frozen through retries. Commit acknowledges exactly those
  versions; changes arriving after fetch remain pending. Never commit merely
  because a query succeeded or a memory write returned without read-back.

# Notification memory batches

Apply the parent skill’s scope, persistence, and reporting workflow. No `--source` is needed: notifications remain the default.

## Fetch and validate one batch

```bash
yoooclaw sync next <scope arguments> --format json
```

Check exit status, parse the JSON object, require `ok=true` and a boolean `done`. Require a nonnegative integer `outsideScopePending`.

- `done=true`: require `totalPending=0`, then report completion for the selected scope.
- `done=false`: validate every condition below before writing memory:
  - `date` is a real `YYYY-MM-DD` date inside the locked scope.
  - `startIndex` and `endIndex` are nonnegative integers.
  - `notifications` is an array of notification objects; `returned` is an integer from 0 to 100 and equals its length.
  - `skippedOnly` is a boolean and is `true` exactly when `returned=0`.
  - `endIndex >= startIndex` and the window size `endIndex - startIndex + 1` is between `max(returned, 1)` and 100. The window can be larger than `returned` because notifications imported with `yoooclaw transfer` (marked `transfer.memoryPolicy="skip-history"`) keep their checkpoint positions but are left out of `notifications`.
  - `remainingInScope` is a nonnegative integer. It excludes this batch and reflects the fetch-time count.
  - The top-level `commitCommand` string exactly equals `yoooclaw sync commit --date D --end-index N`, substituting the validated `date` and canonical decimal `endIndex`. Accept no surrounding whitespace, control characters, extra arguments, or shell syntax.

The current CLI returns notification objects directly and supplies no memory prompt file. If `skippedOnly=true`, the window holds only imported history that is intentionally excluded from automatic memory ingestion: skip memory selection and confirm the batch. Complete this step only with a fully validated batch or a valid completion response.


### Attribution and selection

These are incoming messages: the user is the recipient. Preserve sender/app, subject, and date where needed to support the fact. “Please send the proposal tomorrow” supports “the sender requested a proposal,” not “the user promised to send it.” A sender's opinion, plan, or status belongs to that sender. Record a user's completed action only when the source explicitly supports it, retaining attribution.

Merge duplicates across existing memory and the batch. Keep each fact in one layer according to its subject and lifetime:

| Layer | Keep |
| --- | --- |
| Personal profile | Stable facts about the user: identity, responsibilities, project role, well-supported preferences and recurring habits |
| Daily record | Useful date-bound facts under the notification's date: important communication context, confirmed events, project progress, temporary arrangements or one-off tasks |
| Long-term memory | Durable project/organization context, goals and constraints, stable relationships and roles, lasting shared decisions, recurring dates and collaboration rules |

Evaluate all layers; update only those with qualifying evidence. A single recommendation, marketing label, hearsay, or incoming request does not establish a user trait or commitment. If new evidence conflicts with an existing fact and cannot resolve it, preserve the old fact and record sourced uncertainty in the daily layer only when useful.

Discard ads, promotions, routine news/entertainment/weather, network status, verification codes, and routine delivery tracking. Keep raw message logs and lengthy quotations out of memory. Temporary discussion, one-off tasks, and speculative or soon-expiring information do not qualify as long-term memory. A one-time important date belongs in its daily record.


## Exact-index confirmation

Construct a fixed command from the validated fields; never execute or `eval` the returned string:

```bash
yoooclaw sync commit --date <validated-date> --end-index <validated-endIndex> --format json
```

Use argument-array execution when available; otherwise safely quote arguments, including any selected profile. Always pass the exact batch end index. Require a successful exit, `ok=true`, matching `date` and `committedIndex`, and `commitMode="exact-end-index"`.

An exact-index commit is idempotent. If execution was interrupted and its outcome is unknown, retry the identical commit once. If the result is still unknown, stop and report that uncertainty; do not fetch another batch.

Follow the parent skill’s per-source batch budget: at most three substantive notification batches per run, independently of other sources. A `skippedOnly` batch does not count toward the notification budget; stop after 20 consecutive skipped-only confirmations and report the remainder.

Each `next` reads current pending data; the CLI has no run-wide frozen snapshot. Newly arrived notifications may appear in later batches. Commit only the fetched end index and stop when its fetch-time remainder is zero, rather than polling for new arrivals. Never edit checkpoint files or calculate a replacement consumption index.

# Synchronized context queries and memory consumption

The context-query skill now routes calendars, locations and health metrics alongside existing notifications and web pages. The renamed context-to-memory skill covers exactly notifications, calendars, locations, health-data and web-pages. Recording/image queries remain available but are excluded from this memory workflow.

## Read-only commands

- `calendar list`: future starts only, ordered by start instant. The response says `scope=upcoming`, explains the default, and records `asOf`. Ongoing events are excluded. `isAllDay` identifies all-day events; legacy `allDay` is a fallback. Explicit false in the current field takes precedence. All-day events start at source-local midnight.
- `calendar list --all`: all synchronized past, ongoing and future events (`all_synced`). This is not a claim of complete device history.
- `calendar list --date YYYY-MM-DD` or `--from-date ... --to-date ...`: events overlapping the explicit source-local dates, overriding the upcoming default. `--all` conflicts with date filters.
- `calendar get <id>`: resolve an ID from list. IDs fingerprint content, because ingestion does not require stable event IDs; resolve again after a revision. Original event/instance identities, if supplied, remain in `data`.
- `location list` / `get <id>`: original observations. Dates select receipt days, and `timestamp` is unvalidated observation time (legacy `collectedAt`); `accuracyMeters`, `poi`, `provider`, and `deviceType` remain in the original data. Exact duplicate rows within a receipt day collapse.
- `health-data list`, `get --date ...`, `series --metric ...`: current daily snapshots or metric series. Missing values stay null, with units/provider and partial-day metadata preserved. Legacy health receipts are excluded.

Lists return `coverage.connected`, actual `availableDates`, `timeBasis`, `total`, and `nextCursor`. The default page is 100 items (`--limit 1..500`). Follow the cursor with unchanged filters. Source/query changes invalidate a cursor so pages cannot silently mix versions. Queries do not write consumption state.

## Memory protocol

Existing notification `sync next/fetch/commit` remains index-based and remains the default source, including imported-history exclusions. For the four additional sources:

```sh
yoooclaw sync next --source health-data --date 2026-01-15 --format json
yoooclaw sync commit --source health-data --token <returned-token> --format json
```

`--source` accepts calendars, locations, health-data, web-pages and notifications. `scan/next` accept the existing date scope flags (default local today). Calendar memory scope is snapshot days, so future plans require an explicit future range or `--all`. Health uses source-local dates, locations receipt dates, and pages latest capture dates. Historical backfills/revisions outside a selected scope are counted separately; `--all` includes them.

New sources maintain independent state under the profile's `state/memory-sync/`. A process lock serializes state updates. `next` persists a frozen batch, including exact material and version; retries return it until committed. Changing scope while a batch is pending fails explicitly. `commit` accepts only the issued source/profile token and writes state atomically; retry of the most recent confirmation is idempotent. Later upstream changes remain pending. Corrupt source/state files fail rather than reset progress. A batch contains at most 100 records and normally 64 KB; one larger source document is returned intact.

- Locations use receipt-day/content identity. Timestamp/source fields are not fabricated.
- Health uses one latest snapshot per business date; provider/timezone/metrics determine content version, not updatedAt alone.
- Calendars consume complete per-day snapshots, including empty files, and return the previously confirmed day for reconciliation. This avoids inventing mandatory event identity fields missing from the upstream contract. Cross-day duplicate events must be reconciled by the model. Missing files are not deletion events; absence from a covered day is source removal, not proof of cancellation. Upstream multi-day writes are not transactional, so the model must not infer cross-day cancellation from one observed day.
- Web pages use URL identity and actual Markdown/title content version. Generated frontmatter timestamps do not cause re-consumption. Current metadata remains evidence of capture, not proof of reading. Unsafe/missing material paths fail. Historical versions are queried with existing webpage commands.

Persistent memory is managed by the host agent, not the CLI. The skill requires read-before-write merging, read-back verification and explicit attribution before confirmation. Every modified destination requires a separate post-write Read whose contents are inspected before a later confirmation call; a successful Write response alone is insufficient. Host indexes created during finalization also require an actual read-back before reporting success. Retries deduplicate partial memory writes. Thread ingestion cursors remain independent. This change does not install Thread source adapters or alter role scheduling.

Each user-triggered memory run allows up to three substantive batches **per selected source**, counted independently. Reaching one source’s limit does not prevent processing the next source; five selected sources allow at most fifteen substantive batches. Report remaining work by source. Notification skipped-only windows retain their separate limit. These are skill execution rules, not CLI-enforced limits.

## Skill rename

`skills install` installs `yoooclaw-context-to-memory` and then removes the legacy notification-only directory. The legacy Wuying executable/profile binding is preserved when creating the new skill. Existing notification checkpoint files are untouched. Deployment still requires installing the updated CLI and refreshing its bundled skills; Git changes alone do not activate them.

## Validation

Synthetic isolated tests cover upcoming/all/date scopes, overlapping calendar days, pagination changes, exact-version commit while new data arrives, independent source/profile tokens, repeated confirmations, timestamp-only health updates, calendar empty snapshots, missing health metrics, location duplicate rows, source/checkpoint corruption, webpage updates/path boundaries, command dispatch and skill rename binding preservation. No production model, private data, or remote delivery is needed for these tests.

## Source-field compatibility validation

Query fixtures cover the current `isAllDay`, `timestamp`, `accuracyMeters`, `poi`, and source metadata fields, with older optional spellings retained. Calendar sorting and upcoming filtering share the same effective-start calculation. Location scope remains receipt-day based even when observation time falls on another day. An isolated local profile can be populated with copied source folders to exercise the real CLI; keep private samples and reports outside Git. Run query and pagination checks before any test-only consumption, and verify that token confirmation never changes the source files.

## Empty-scope handling

Memory runs retain the user-selected date arguments across every source and follow-up query. Empty results, unavailable sources and out-of-scope pending counts do not authorize a broader query. A completed source advances directly to the next selected source; an entirely empty run reports coverage without creating memory or index files. Final replies describe only the five supported memory sources.

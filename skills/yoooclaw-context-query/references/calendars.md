# Calendar queries

Apply the parent query skill. These commands read local synchronized files without changing memory progress.

```bash
yoooclaw calendar list --format json
yoooclaw calendar list --all --format json
yoooclaw calendar list --date YYYY-MM-DD --format json
yoooclaw calendar list --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
yoooclaw calendar get <id-from-list> --format json
```

Default `list` returns only events starting strictly after `asOf`, ordered by start time; already-started ongoing events are excluded. All-day events start at midnight in their source time zone. The response explicitly identifies `scope="upcoming"` and explains the default. Use `--all` for all synchronized past/ongoing/future events (`all_synced`), or explicit dates for overlap with those source-local days (`date_range`). `--all` and date filters are mutually exclusive. A query ID identifies the returned content; after an edit, list again to resolve the current record.

Read `coverage.connected` and `coverage.availableDates`: synchronized empty days differ from unsynchronized days. Coverage is local availability, never a claim of complete device history. Exact cross-day duplicate events are returned once. Original source fields remain in `data`, including source event/instance IDs when present. Preserve source time zones and all-day meaning; an event in the source is a plan, not evidence of attendance.

Use `--limit 1..500` (default 100). If `nextCursor` is non-null, repeat the same filters with `--cursor <value>` until the requested range is covered. The cursor retains the first page’s asOf. A source change invalidates pagination; restart instead of mixing versions. Report partial coverage if the source keeps changing.

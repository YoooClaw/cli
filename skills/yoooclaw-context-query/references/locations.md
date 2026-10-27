# Location queries

Apply the parent query skill.

```bash
yoooclaw location list --date YYYY-MM-DD --format json
yoooclaw location list --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
yoooclaw location list --all --format json
yoooclaw location get <id-from-list> --format json
```

Dates select receipt-day files. Each result retains original fields in `data`, plus id/date/version/ref. Current records use `timestamp` for observation time, `accuracyMeters` for accuracy, `poi`/`address` for place descriptions, and `provider`/`deviceType` for source metadata. Older records may use `collectedAt`, `horizontalAccuracy`, or `poiText`; keep the supplied fields and units explicit. Observation time and accuracy may be absent or invalid: ingestion only guarantees valid longitude/latitude. Distinguish receipt date from observation time and report unknown fields. Exact same-day duplicates are collapsed. Querying a received-day range cannot prove complete observation-time coverage when late uploads are possible.

Read `coverage` and `timeBasis`. Interpret points as observations; place roles, visits, and habits require supporting context. Use bounded date ranges for questions. Follow `nextCursor` with unchanged filters and `--cursor`; `--limit` defaults to 100 and permits 1..500. Changed data invalidates the cursor, requiring a fresh scan. A readonly query never marks observations consumed.

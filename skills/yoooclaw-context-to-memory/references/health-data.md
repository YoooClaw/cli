# Health memory

Use `sync next --source health-data` and token confirmation from the parent skill. Each item is the latest snapshot for one business date. `data.header` supplies date/timeZone/provider/updatedAt; `data.items` supplies metric/name/value/unit. Previous data is the last confirmed snapshot. Timestamp-only updates do not produce new consumption.

Update the existing dated health record when metrics change; retain evidence of revision where useful. Missing metrics mean unread/unknown, not zero. A day matching the source-local current day is partial as of updatedAt. Keep metric, unit, provider, and date when comparing; only compare equivalent definitions. Daily values can become dated records; durable trends need enough comparable observations and do not establish a diagnosis.

For metric definitions, read [health query semantics](../../yoooclaw-context-query/references/health-data.md#metric-semantics).

# Health queries

Apply the parent query skill.

```bash
yoooclaw health-data list --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
yoooclaw health-data get --date YYYY-MM-DD --format json
yoooclaw health-data series --metric steps --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
```

`list` and `get` read only v3 daily snapshots: `data.header` gives date/timeZone/provider/updatedAt; `data.items` contains ordinary metric/name/value/unit. `data.sleepSessions` contains sleep summaries with segmentCount in `list`; `get --date` includes complete segments (one daily file, at most 4 MiB on disk). Read details only when needed for stage order or wake times. `series` returns one selected metric per synchronized date, preserving its unit and provider; absent metrics return `measurement=null, missing=true`, not zero. Unsynchronized dates are described by gaps in coverage, not fabricated rows. `partialDay` marks the current source-local day as of the query time. Report freshness using updatedAt.

Dates refer to source-local business days. Read current snapshots afresh; combine days only with matching metric definitions and units. Daily updates replace earlier values. Old snapshot formats are unsupported and return an error; do not rewrite their version headers. Use the metric semantics below for sleep, exercise, percentages, and cross-platform comparisons.

`--limit` is 1..500, default 100. Follow `nextCursor` with the same query and `--cursor` to cover the requested range; restart if data changed. Queries leave consumption progress untouched.

## Metric semantics
- steps is the system-merged daily total. active_energy excludes resting energy.
- walking_running_distance and cycling_distance are separate categories; flights_climbed is floors.
- apple_exercise_minutes is the exercise ring, not workout session duration.
- *_average/minimum/maximum/latest retain their distinct meanings; HRV SDNN and RMSSD remain separate.
- oxygen_saturation_average and body_fat_latest use percent: 98 means 98%.
- Sleep sessions belong to the local end date and are not split at midnight. sleepMinutes already includes core/deep/rem/unspecified stage totals; never add them again or sum by sources. unknownMinutes is unobserved time, not awake or asleep. isPartial only marks a query boundary; false does not prove complete device synchronization or that the user woke up. Start/end are observed sleep boundaries, not bedtime/rise time. Do not use old sleep_* metrics for session trends.
- workout_*_minutes belongs to the workout start date and excludes native pauses; do not add it to exercise-ring minutes.

Client source merging has already occurred. Use current v3 daily snapshots without accumulating different revisions.

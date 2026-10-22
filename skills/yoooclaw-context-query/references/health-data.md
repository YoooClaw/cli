# Health queries

Apply the parent query skill.

```bash
yoooclaw health-data list --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
yoooclaw health-data get --date YYYY-MM-DD --format json
yoooclaw health-data series --metric steps --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
```

`list` and `get` return current daily snapshots: `data.header` gives date/timeZone/provider/updatedAt; `data.items` contains metric/name/value/unit. `series` returns one selected metric per synchronized date, preserving its unit and provider; absent metrics return `measurement=null, missing=true`, not zero. Unsynchronized dates are described by gaps in coverage, not fabricated rows. `partialDay` marks the current source-local day as of the query time. Report freshness using updatedAt.

Dates refer to source-local business days. Read current snapshots afresh; combine days only with matching metric definitions and units. Daily updates replace earlier values. Legacy receipt batches are excluded. Use the metric semantics below for sleep, exercise, percentages, and cross-platform comparisons.

`--limit` is 1..500, default 100. Follow `nextCursor` with the same query and `--cursor` to cover the requested range; restart if data changed. Queries leave consumption progress untouched.

## Metric semantics
- steps is the system-merged daily total. active_energy excludes resting energy.
- walking_running_distance and cycling_distance are separate categories; flights_climbed is floors.
- apple_exercise_minutes is the exercise ring, not workout session duration.
- *_average/minimum/maximum/latest retain their distinct meanings; HRV SDNN and RMSSD remain separate.
- oxygen_saturation_average and body_fat_latest use percent: 98 means 98%.
- sleep_minutes covers local midnight-to-midnight, not necessarily “last night”; stage minutes are already included. Preserve core versus light distinctions.
- workout_*_minutes belongs to the workout start date and excludes native pauses; do not add it to exercise-ring minutes.

Client source merging has already occurred. Use current daily snapshots, excluding legacy raw archives from sums.

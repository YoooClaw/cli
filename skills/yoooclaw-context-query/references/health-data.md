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
- Sleep sessions belong to the local end date in timeZone, without splitting at midnight. startAt/endAt are observed sleep boundaries, not bedtime/rise time: do not call their span time in bed or use it to derive sleep efficiency.
- isPartial marks touching the collection query boundary. false does not establish a complete night, complete device synchronization, or that the user woke up. It is distinct from partialDay (the current source-local day); do not infer isPartial from today's date.
- unknownMinutes is unobserved time in the recorded timeline, neither awake nor asleep. For zero, say only “当前时间线未标出未知区间”; do not translate this into continuous device acquisition, no dropped data, or a complete night.
- sleepMinutes already includes core/deep/rem/unspecified stageMinutes; never add stages again or sum by sources. Do not use old sleep_* metrics for session trends.
- Segment endpoints are rounded separately to minutes (30 seconds rounds up), then differences are summed. Totals can differ from subtracting the whole window; awakeningCount may include intervals rounded to zero minutes.
- Report segment counts from segmentCount or a computed length of segments, not by visually counting long JSON. Preserve supplied aggregate values; calculate any new value from the returned data before reporting it.
- Describe the observed first/last stages without calling their order typical, normal or abnormal. Keep dated observations separate from interpretations; a single snapshot does not establish long-term habits. Before the final answer, check every reported date, count, unit and value against the returned data, and check field explanations against this reference.
- workout_*_minutes belongs to the workout start date and excludes native pauses; do not add it to exercise-ring minutes.

Client source merging has already occurred. Use current v3 daily snapshots without accumulating different revisions.

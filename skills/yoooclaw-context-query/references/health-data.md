# Health queries

Apply the parent query skill.

```bash
yoooclaw health-data list --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
yoooclaw health-data get --date YYYY-MM-DD --format json
yoooclaw health-data series --metric steps --from-date YYYY-MM-DD --to-date YYYY-MM-DD --format json
```

## Read and interpret

1. Read the returned `sourceSemantics` before interpreting health fields. The CLI
   automatically includes common rules and the types present on this page.
   Normal data queries need no additional schema command. Apply these rules to the
   answer; these definitions are metadata, not observations about the user.
2. Use `get --date` when the question needs complete sleep segments, dose events,
   intake entries or mood records. `list` contains summaries; `series` selects
   daily metrics. Check the full result's `sourceSemantics` too.
3. Follow `nextCursor` with the same filters to cover the requested range.
   Restart pagination if the CLI reports changed data. Queries leave consumption
   progress untouched.
4. Check each reported date, count, value and unit against the returned data.

For field-definition questions without a data query, or when reading JSONL files
that have no attached metadata, use:

```bash
yoooclaw health-data schema --format json
yoooclaw health-data schema --type sleepSessions --format json
```

`schema` lists supported types and their explanations without reading user data.
Use its `matchKeys` to map full or summarized data to sections. It describes
interpretation rules, not a complete JSON Schema validator. If explanations are
unavailable, report that limitation before making claims about field meanings.

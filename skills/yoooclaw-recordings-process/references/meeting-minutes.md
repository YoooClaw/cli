# Meeting minutes

Apply the parent language rules and use only `selectedSources`.

## Parse and extract

1. Rewrite spoken fragments as concise written language.
2. Remove fillers, repetitions, greetings, and repeated confirmations.
3. Capture topics, decisions, action items, risks, and unresolved points.
4. Preserve speaker attribution only when supported.

## Output

Use `replyLanguage` for headings, labels, table headers, and status values.

```markdown
# <meeting-minutes heading>: [topic / filename]

> **<overview label>**
> - **<source label>**: `filename.md`
> - **<time label>**: [source-backed time]
> - **<participants label>**: [identifiable roles]

## <topics-and-decisions heading>
- ...

## <action-items heading>
| <item> | <owner> | <due date> | <status> |
|---|---|---|---|

## <risks-and-open-items heading>
- ...
```

Do not invent owners, deadlines, participants, decisions, or status. Use a localized “not specified” value when absent. Produce separate minutes for several recordings unless the user requests one merged report.

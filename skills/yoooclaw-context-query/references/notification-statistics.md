# Notification statistics

Apply the parent `yoooclaw-context-query` rules. Read this branch only for counts, rankings, distributions, or trends.

## 1. Set dimensions

Map the request to:

- date range: today, last 7 days, last 30 days, all, or explicit dates;
- app/sender/client filter;
- dimension: `date`, `app`, `sender`, `hour`, `client`, or `all`.

Default to the last 7 days and `all` only when the user did not specify them. Resolve relative dates from the local system date.

## 2. Run one aggregate command

```bash
yoooclaw notification stats --dim all --from <DATE_OR_ISO> --to <DATE_OR_ISO> --format json
yoooclaw notification stats --dim sender --sender "<sender>" --from <ISO_TIME> --to <ISO_TIME> --format json
```

Add `--app`, `--sender`, or `--client` only for explicit filters. Prefer CLI aggregates over reading or grouping raw storage files.

The result can include `range`, `total`, `byDate`, `byApp`, `bySender`, `byHour`, and `byClient`.

## 3. Report

- State the actual date range and total.
- Include only requested distributions.
- Calculate percentages from returned totals and keep arithmetic consistent.
- Identify peak dates/hours only when supported by the result.
- Keep source identifiers and timestamps unchanged.

Do not route an ordinary “what happened recently” summary through statistics.

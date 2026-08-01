# Notification queries

Apply the parent `yoooclaw-context-query` language, freshness, profile, output-format, and read-only rules.

## Route

- Summary, digest, recent overview, or “what happened lately”: use the summary flow.
- Sender, app, keyword, time range, or a small recent list: use the focused-query flow.
- Counts, rankings, distributions, or trends: also read `notification-statistics.md`.

Treat “unread” as “recent” because the current notification schema has no independent read/unread field.

## Summary flow

### 1. Set the scope

Let `X` be the requested count. Default to 700 when no count is given.

- `X ≤ 3000`: set `N=X`.
- `X > 3000`: set `N=3000` and disclose that only the most recent 3000 notifications are covered.
- Convert a previous-summary time such as `YYYY年M月D日 HH:mm` to ISO 8601 with the local timezone and pass it as `--from`.
- Resolve today/yesterday/relative dates from the local system date rather than model memory.

### 2. Run exactly one summary path

For `N ≤ 700`:

```bash
yoooclaw notification summary --limit N --sample 12 --top 8 --format json
# With a lower bound:
yoooclaw notification summary --from <ISO_TIME> --limit N --sample 12 --top 8 --format json
```

For `N > 700`:

```bash
yoooclaw notification summary-job create --limit N --chunk-size 150 --max-content 120 --format json
# Add --from <ISO_TIME> to create when required.
yoooclaw notification summary-job run <id> --max-chunks 30 --include-result --format json
```

Use the returned `markdown` or `resultFile`. When the user explicitly requests higher-quality manual chunk synthesis, use `summary-job next` followed by `commit` for one chunk at a time, then `result`. Never accumulate several raw chunks in context.

When the requested count is 50 or more, use this summary flow. Do not fetch hundreds of complete notifications with `search`.

### 3. Present the summary

1. Give a compact overall overview.
2. Group by app or sender.
3. Keep each group to 3–5 points.
4. State actual count and time coverage.

Treat samples and top senders as representative, not as the entire result.

## Focused-query flow

### 1. Map filters

- Time range → `--from` / `--to` with ISO 8601 values.
- App wording → `--app`; keep the user’s wording because aliases are resolved by the CLI.
- Named person → `--sender`.
- Content phrase → `--keyword`.
- Requested count → `--limit`.
- Source device/profile → `--client` / global `--profile`.

### 2. Query

Confirm the current path only when it is useful:

```bash
yoooclaw notification storage-path --format json
```

Choose one focused command:

```bash
yoooclaw notification search --limit 20 --format json
yoooclaw notification search --from <ISO_TIME> --to <ISO_TIME> --format json
yoooclaw notification search --app "<app>" --format json
yoooclaw notification search --sender "<sender>" --limit 20 --format json
yoooclaw notification search --keyword "<keyword>" --format json
yoooclaw notification +today --format json
yoooclaw notification +recent --format json
```

Show only the requested result window and preserve chronological meaning.

## Storage and result contract

The CLI resolves the active profile to `~/.yoooclaw/profiles/<profile>/notifications`. New data is stored as one JSON object per line in `YYYY-MM-DD.jsonl`; legacy `YYYY-MM-DD.json` arrays remain readable.

Common fields:

- `appName`, `appDisplayName`
- `title`, `content`
- `timestamp`
- `senderName`
- `conversationType`, `conversationName`
- `clientLabel`

Do not walk storage files when the CLI can answer the query. Empty search or summary results are valid and mean no matching notification was found.

Command failures use:

```json
{"ok":false,"error":{"code":"YOOOCLAW_...","message":"...","hint":"..."}}
```

Correct invalid parameters from `message`/`hint`; do not hide failures.

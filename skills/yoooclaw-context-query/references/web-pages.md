# Captured web-page queries

Apply the parent `yoooclaw-context-query` rules.

## Source priority

For the user's saved, bookmarked, viewed, read, opened, or captured articles/pages, query `yoooclaw synced-web-page` first. Do not first use browser automation, browser sessions, or raw bookmark/favorites/history files. Use another collection platform only when the user explicitly names it.

The captured copy is local historical context, not proof of the current live Internet page.

## Commands

```bash
yoooclaw synced-web-page storage-path --format json
yoooclaw synced-web-page list [--from <ISO_TIME>] [--to <ISO_TIME>] [--client <label>] --format json
yoooclaw synced-web-page search "<keyword>" --limit <requested-count-or-total-pages> [--client <label>] --format json
yoooclaw synced-web-page path <urlHash> --format json
```

`yoooclaw synced-web-page list` returns newest capture first with fields including `urlHash`, `title`, `siteName`, `canonicalUrl`, `capturedAt`, `firstCapturedAt`, `captureCount`, `relativePath`, and `hasArchive`. Its optional ISO 8601 `--from` boundary is inclusive and `--to` boundary is exclusive. Pass `--client <label>` only for an explicit source filter (`all` disables it); pages captured before client labels existed report `legacy`.

`yoooclaw synced-web-page search` searches title, site name, URL, canonical URL, and Markdown body. It intentionally does not search raw HTML archives or access the Internet.

## Route by intent

### Recent or time-bounded pages

1. Convert the requested range to ISO 8601 boundaries in the current local timezone:
   - 最近一天/24 小时: rolling 24-hour window ending now;
   - 今天/昨天: local calendar day;
   - 近 N 天/周/月: rolling interval ending now.
2. Run `yoooclaw synced-web-page list --from <ISO_FROM> --to <ISO_TO> --format json`. Omit one flag for an open-ended range.
3. Return the command's matching metadata in its existing `capturedAt` descending order.

Generic collection words such as “收藏”, “保存”, “看过”, or “文章” describe the collection operation; do not pass them literally to `yoooclaw synced-web-page search` unless the user is searching those words inside page content.

### Keyword search

Run `yoooclaw synced-web-page list` first to obtain the total number of synchronized pages. Run `yoooclaw synced-web-page search` with the substantive topic phrase and the user-requested count, or use the total page count when no count was requested so the CLI default does not truncate matches. Present page title, site, canonical URL, capture time, matched metadata fields, and short body snippets.

### Read or answer from one page

1. Identify a unique page through `yoooclaw synced-web-page list` or `yoooclaw synced-web-page search`.
2. Run `yoooclaw synced-web-page path <urlHash>`.
3. Read the returned Markdown file.
4. Answer from that file and include its `canonicalUrl`.

Prefer Markdown. Read an HTML archive only when explicitly requested and when its path is available through trusted metadata.

## Result rules

- `capturedAt` is capture time, not necessarily publication time.
- Disclose `truncated: true` or `lowConfidence: true` from frontmatter.
- Preserve page titles and canonical URLs.
- Do not edit Markdown, HTML, or `index.json`.
- If freshness matters, separately use a live external web source and distinguish it from the captured copy.

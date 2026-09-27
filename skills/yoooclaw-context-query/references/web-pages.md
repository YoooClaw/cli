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
yoooclaw synced-web-page path <urlHash> [--version <N>] --format json
yoooclaw synced-web-page versions <urlHash> --format json
yoooclaw synced-web-page diff <urlHash> [--from <N>] [--to <N>] [--context <lines>] [--max-lines <lines>] --format json
```

`yoooclaw synced-web-page list` returns newest capture first with fields including `urlHash`, `title`, `siteName`, `canonicalUrl`, `capturedAt`, `firstCapturedAt`, `captureCount`, `relativePath`, `hasArchive`, `versionCount`, `lastChangedAt`, and `tracking`. `captureCount` counts every save; `versionCount` counts saves whose content actually changed, so `versionCount > 1` means the page has earlier versions on disk. Its optional ISO 8601 `--from` boundary is inclusive and `--to` boundary is exclusive. Pass `--client <label>` only for an explicit source filter (`all` disables it); pages captured before client labels existed report `legacy`.

`yoooclaw synced-web-page search` searches title, site name, URL, canonical URL, and the latest version's Markdown body. It intentionally does not search earlier versions, raw HTML archives, or the Internet.

`yoooclaw synced-web-page versions` lists one page's versions in `capturedAt` ascending order. Each version has `version`, absolute `path`, `capturedAt`, `firstSeenAt`, `lastSeenAt`, `observationCount`, `changedFromPrev` (`ratio`, `added`, `removed`; `null` on the first version), `current`, and optionally `late` and `note`. `history: false` means the page was never versioned: it has one body and no earlier state.

`yoooclaw synced-web-page diff` compares two versions line by line and returns `from`, `to`, `added`, `removed`, `unchanged`, `approximate`, `truncated`, and a unified-style `patch` (`-` old line, `+` new line, `@@` between hunks). Without flags it compares the latest version with the version before it. Whitespace-only and blank-line differences are ignored.

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

### Changes, comparisons, and trends of one page

Use this route when the user asks how a saved page changed: “这个看板这周变了什么”, “上次我看的时候是什么样”, “营收涨了多少”, “和上周比有什么不同”, or asks for a page's history or trend.

1. Identify a unique page through `yoooclaw synced-web-page list` or `yoooclaw synced-web-page search`.
2. Run `yoooclaw synced-web-page versions <urlHash>`.
   - `history: false` or one version: say the page has only one saved copy (with its `capturedAt`) and cannot show change. Do not infer change from memory or from the live page.
3. Pick versions by time, not by version number. A version is valid from its `firstSeenAt` to its `lastSeenAt`, so the state at a moment is the latest version whose `firstSeenAt` is at or before that moment.
   - “上次”/“previous”: the version before the `current` one.
   - “这周”/“since <date>”: the version in effect at the start of the range versus the `current` version.
4. Run `yoooclaw synced-web-page diff <urlHash> --from <N> --to <M>` for the selected pair. Read full bodies (`path --version <N>`) only when the patch is `truncated`, `approximate`, or lacks the context needed to answer.
5. For a trend over several versions, walk consecutive pairs in `capturedAt` order and extract the requested figure from each version; cite each value with its capture time.
6. Answer with what changed, the two capture times compared, and the canonical URL.

Use `changedFromPrev.ratio` only to find which versions changed most; it counts changed lines, not meaning.

## Result rules

- `capturedAt` is capture time, not necessarily publication time.
- Disclose `truncated: true` or `lowConfidence: true` from frontmatter.
- Preserve page titles and canonical URLs.
- Do not edit Markdown, HTML, `index.json`, or anything under `versions/`; saved versions are evidence.
- Order history by `capturedAt`. A version marked `late: true` arrived after newer ones (an offline queue delivered it later), so its number is higher than its place in time.
- `observationCount` counts saves that found the same content. Several observations over a span mean the page stayed unchanged for that span; they are not separate changes.
- A version whose `note` is `pre-versioning` stands for saves made before history existed; there is no body for any state before it. Say so instead of treating it as the page's first appearance.
- While `tracking` is `off` new saves overwrite the current version, so a quiet period in the history can mean tracking was off rather than that nothing changed. Mention this when the gap matters to the answer.
- If freshness matters, separately use a live external web source and distinguish it from the captured copy.

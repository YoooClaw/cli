# Mind map

Apply the parent language rules.

## 1. Lock the source

- Provided text/file: lock it directly.
- Explicit recording/transcript request: use `recording-source.md`.
- No content and no recording request: ask the user to provide text/file or choose a local recording transcript.

## 2. Build the hierarchy

1. Merge repeated statements into stable themes.
2. Remove spoken filler while preserving meaning.
3. Build `topic → subtopic → key point → detail/action` relationships.
4. Distill one central title.
5. Mark source-backed actions, open points, and risks.

## 3. Return one Markdown map

# <mind-map heading>: [topic]

## <mind-map-body heading>
```md
# <central topic>
- <branch A>
  - <subtopic A1>
    - <key point>
- <branch B>
```

> <localized tip: import this Markdown into XMind or a similar tool>

Use localized `[action]`, `[open]`, and `[risk]` tags only when supported. Keep nodes concise. Return separate maps for multiple recordings unless one merged map was requested. Do not add a redundant prose summary, table, or ASCII tree.

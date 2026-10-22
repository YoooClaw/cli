# Location memory

Use `sync next --source locations` and token confirmation from the parent skill. Scope dates are CLI receipt days, not necessarily observation dates. Exact duplicate records within one receipt day are collapsed; other repeated points may still need semantic deduplication.

Read original `collectedAt`, device/source fields, coordinate system and accuracy when present. The ingest contract only validates longitude/latitude: absent or invalid time/accuracy remains unknown. Keep the distinction between observed position and inferred activity. Single points support dated observations, not automatic home/work labels or attendance. Recurring places require corroborating evidence; preserve uncertainty and gaps. Prefer useful bounded activity records over raw coordinate dumps.

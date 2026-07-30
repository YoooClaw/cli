# Entity extraction

Apply the parent language rules. Accept `selectedSources`, directly provided text, or explicit readable text files.

## Inputs

- User-specified types such as phone numbers, people, organizations, or brands: extract only those types.
- Unspecified: extract people, contact information, organizations/companies, products/brands, dates/times, and technical terms.

## Rules

1. Adjust focus from the user’s request.
2. Deduplicate repeated entities.
3. Keep entity values in the source language.
4. Give each entity a short representative source context.
5. Do not infer entities absent from the source.

## Output

- Recording source: `<storage-path>/entity/<date>_<brief_summary>_entities.json`.
- Explicit file: write `<basename>_entities.json` beside the source. Also write `<recording-storage>/entity/<basename>_entities.json` when `yoooclaw recording storage-path --format json` succeeds.
- Provided text: return JSON in chat unless a destination was requested.

Both copies for an explicit file must be identical. Include:

```json
{
  "source_file": "...",
  "extracted_at": "...",
  "user_request": "...",
  "entity_types_extracted": [],
  "entities": []
}
```

Report every written path. If an output already exists, overwrite only as part of the requested artifact generation and mention it.

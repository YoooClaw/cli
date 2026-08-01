# Translation

Apply the parent language rules. Accept `selectedSources`, directly provided text, or an explicit readable text file.

## Inputs

Determine `targetLanguage`. It controls the translated artifact, not `replyLanguage`.

- Recognize common names/codes such as English/en, Japanese/ja, Korean/ko, French/fr, Spanish/es, and German/de.
- If no target is specified, ask before processing.
- If source and target languages are the same, state that translation is unnecessary and stop.
- Preserve literal timestamp markers such as `**[关键点 MM:SS]**`.

## Two-phase flow

For text longer than 50 characters:

1. Extract `domain`, `tone`, and a glossary of names, companies, technical terms, and abbreviations.
2. Translate the full text using that context and glossary.

For short text, translate directly.

Mandatory rules:

- Never modify timestamp markers.
- Translate metadata labels but preserve timestamps and numeric values.
- Preserve Markdown structure.
- Use glossary terms consistently.

## Output

- Recording source: write `<storage-path>/translation/<date>_<brief_summary>_translation_<lang>.md`.
- Explicit file: write `/path/to/<basename>_translation_<lang>.md` beside it.
- Provided text: return translated Markdown in chat unless a destination was explicitly requested.

If an output exists, overwrite only when the user’s request authorizes generating that artifact and mention the overwrite. Report target language, processed item count, output paths, and whether glossary extraction fell back to basic mode.

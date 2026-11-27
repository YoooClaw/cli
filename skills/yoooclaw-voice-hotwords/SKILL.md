---
name: yoooclaw-voice-hotwords
description: Manage YoooClaw speech-recognition hotwords. Use when asked to list the dictionary, suggest hotwords from a source, add or remove terms, or adjust their recognition weights.
---

# Speech-recognition hotwords

If you're unsure which command to use, run `yoooclaw voice hotwords --help`. For an option or example, open the relevant command's help, such as `yoooclaw voice hotwords update --help`.

## Look up a term

Run `yoooclaw voice hotwords list --format json` and read `text` and `weight` from `items`. Pass each `next_cursor` unchanged to `--cursor`. For a single term, stop at an exact match. To list the whole dictionary or establish that a term is absent, continue until the response omits `next_cursor`.

Use JSON for pagination and counts. NDJSON streams the current page's term records. Each JSON page's `count` is the number of terms in its `items` array. To report the dictionary size, sum `count` once per page in code. If reporting a breakdown by weight, tally the items from those same pages and verify the subtotals add up to the dictionary size.

## Select terms from a source

Use the source the user specified. For a general context request, use `yoooclaw-context-query` to search original written sources only, such as message senders and text, calendar event titles, and saved web pages. For “my last N days of context”, pass `--last-days N` to each query. Keep recordings, transcripts, recording summaries, dictation history, and excerpts copied from them out of this default search, even when written sources are empty.

Select people, companies, products, acronyms, and industry terms from the selected sources. Deduplicate exact matches and preserve spelling, case, and spaces. A request for suggestions ends with the candidate list. Write the terms when the user asks to add them.

If the selected sources contain no usable context, end the turn. For a Chinese request, copy the following sentence verbatim as the entire reply. For other languages, return only its translation.

> 暂无可用于整理热词的上下文。

## Choose a weight

Weight is an integer from 1 to 5. It controls how strongly recognition favors the hotword when several spellings or words fit the sound. Use the user's requested value when given.

| Weight | When to use it |
| --- | --- |
| 1 | The lightest preference. Use for a rare name when its everyday homophone is usually what the speaker means. |
| 2 | A slight preference. Use for an occasional term when ordinary speech still needs to come through reliably. |
| 3 | A moderate preference. Use when both a term and its sound-alike occur regularly. |
| 4 | The default for new terms. A good starting point for a name, product, or technical term the speaker uses often. |
| 5 | The strongest preference. Use when a recurring term still comes back as its sound-alike at 4. |

For example, if the project name “Sail” keeps appearing as “sale” at 4, try 5. If ordinary mentions of “sale” start appearing as “Sail”, lower the weight with `update`.

## Change the dictionary

| Intent | Command | Result |
| --- | --- | --- |
| Add terms | `yoooclaw voice hotwords add "Sail" "North Star" --format json` | New terms get the default weight unless you pass `--weight`. Existing terms keep their weight. |
| Adjust weights | `yoooclaw voice hotwords update "Sail" "North Star" --weight 5 --format json` | Sets the explicit weight on existing terms. A missing term returns HTTP 404. |
| Remove terms | `yoooclaw voice hotwords delete "Sail" "North Star" --format json` | A term that is already absent also counts as success. |

Pass each term as one argument. Group additions or updates by target weight. The CLI submits each term in order, so a failed batch can include successful writes. Read `results` on success or `error.results` on partial failure and report which terms were `ok`, `error`, or `not_attempted`. If the connection drops, check the affected terms with `list` before retrying.

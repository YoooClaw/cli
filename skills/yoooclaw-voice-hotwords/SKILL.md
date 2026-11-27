---
name: yoooclaw-voice-hotwords
description: Manage YoooClaw speech-recognition hotwords. Use when asked to list the dictionary, suggest hotwords from a source, or add or remove terms.
---

# Speech-recognition hotwords

If you're unsure which command to use, run `yoooclaw voice hotwords --help`. For an option or example, open the relevant command's help, such as `yoooclaw voice hotwords add --help`.

## Look up a term

Run `yoooclaw voice hotwords list --format json` and read `text` from each entry in `items`. Pass each `next_cursor` unchanged to `--cursor`. For a single term, stop at an exact match. To list the whole dictionary or establish that a term is absent, continue until the response omits `next_cursor`.

Use JSON for pagination and counts. NDJSON streams the current page's term records. Each JSON page's `count` is the number of terms in its `items` array. To report the dictionary size, sum `count` once per page in code.

## Select terms from a source

For “my last N days of context”, use `yoooclaw-context-query` to query original written sources within `--last-days N`, such as message senders and text, calendar event titles, and saved web pages. Honor any narrower source the user specifies. Exclude recording transcripts, recording summaries, and speech-to-text history when collecting candidates, including excerpts copied into those written sources. Repeated recognition errors can otherwise enter the dictionary as preferred spellings.

Select people, companies, products, acronyms, and industry terms whose exact spelling appears in the eligible text. Deduplicate exact matches and preserve spelling, case, and spaces. If the only available source is speech-derived, ask for the intended spellings or an original written source. A request for suggestions ends with the candidate list. Write the terms when the user asks to add them.

## Change the dictionary

| Intent | Command | Result |
| --- | --- | --- |
| Add terms | `yoooclaw voice hotwords add "Sail" "North Star" --format json` | Adding an existing term also counts as success. |
| Remove terms | `yoooclaw voice hotwords delete "Sail" "North Star" --format json` | A term that is already absent also counts as success. |

Pass each term as one argument. The CLI submits each term in order, so a failed batch can include successful writes. Read `results` on success or `error.results` on partial failure and report which terms were `ok`, `error`, or `not_attempted`. If the connection drops, check the affected terms with `list` before retrying.

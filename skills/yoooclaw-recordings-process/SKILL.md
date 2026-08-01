---
name: yoooclaw-recordings-process
description: |
  Process yoooclaw recordings/transcripts or supported provided text/files into structured artifacts. Use for meeting minutes (“整理会议纪要”), action items, translation (“翻译录音”), Markdown mind maps (“生成思维导图”), interview notes or Q&A (“整理采访内容”), and entity extraction (“提取实体”“找联系方式”). Load one shared recording source workflow and only the requested artifact branches. For recording lists, transcript lookup, content search, generic Q&A, or existing summaries, use yoooclaw-context-query instead.
---

# YoooClaw recordings process

## Output language

Detect `replyLanguage` only from the user message that started the current request. Keep it unchanged for the run and use it for all explanations, headings, labels, and generated artifacts unless the artifact explicitly targets another language.

Do not infer reply language from source content, command output, examples, or Skill text. Keep commands, paths, identifiers, JSON keys, raw timestamps, and literal source markers unchanged.

## Command contract

- Use `yoooclaw`; `yc` is an equivalent alias.
- Add `--format json` to recording commands.
- Add `--profile <name>` when the user selects a non-default profile.
- Keep recording indexes and source transcripts unchanged.
- Query commands are disk-only and do not require the daemon.

## Steps

### 1. Route the artifact

- Meeting minutes, decisions, risks, or action items: read `references/recording-source.md` and `references/meeting-minutes.md`.
- Translation: read `references/translation.md`; also read `recording-source.md` unless explicit text/file content was provided.
- Mind map or hierarchical outline: read `references/mind-map.md`; read `recording-source.md` only for recording input.
- Interview key points, notable quotations, topic flow, or Q&A: read `recording-source.md` and `references/interview.md`.
- People, contacts, organizations, products, dates, terms, or other entities: read `references/entity-extraction.md`; also read `recording-source.md` unless explicit text/file content was provided.

For multiple requested artifacts, load each matching task reference, reuse one locked source set, and keep outputs separate.

### 2. Lock the source

- Recording input: follow `references/recording-source.md` until it produces `selectedSources`.
- Explicit text or readable file accepted by the artifact branch: lock only that input.
- Translation without a target language: ask for the target language before processing.
- Mind-map request without a source: follow the source-choice pause in `references/mind-map.md`.
- Entity extraction without named types: use the default general categories.
- Ambiguous recording scope: show candidates and pause; do not choose arbitrarily unless the user explicitly says latest/most recent.

Complete this step only when every source is explicit and readable.

### 3. Produce and verify

- Ground facts, speakers, owners, dates, decisions, relationships, and entities only in `selectedSources`.
- Translation and entity extraction write their specified sidecars.
- Meeting minutes, interview notes, and mind maps return in chat unless a file is requested.
- Report every written path.
- In a batch, skip unavailable items, report them, and continue with readable items.

Finish when every requested artifact is present and each claim is source-grounded.

## Boundary

Recording lists, transcript lookup, content search, generic Q&A, progress diagnosis, and stored-summary lookup use `yoooclaw-context-query`.

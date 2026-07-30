# Interview restructuring

Apply the parent language rules and use only `selectedSources`.

## Parse

1. Turn spoken fragments into clear prose; remove fillers and off-topic chatter.
2. Distinguish interviewer and interviewee only when context supports it.
3. Pair questions and answers by meaning, not mechanically by utterance.
4. Extract key points, notable quotations, topic flow, positions, judgments, experience, and examples.

## Output

```markdown
# <interview-notes heading>: [topic]

> **<overview label>**
> - **<source label>**: `filename.md`
> - **<time label>**: [...]
> - **<interviewee label>**: [...]
> - **<interviewer label>**: [...]
> - **<core-theme label>**: [...]

## <key-points heading>
- ...

## <notable-quotes heading>
- ...

## <topic-flow heading>
1. ...

## <Q&A heading>
**Q1:** ...
**A1:** ...

## <additional-observations heading>
- ...
```

Use `replyLanguage` for headings and Q/A prefixes. Preserve meaning and tone without inventing identity, time, background, or quotations. Use a localized “not specified” value for missing metadata. Produce separate outputs for multiple interviews unless merging was requested.

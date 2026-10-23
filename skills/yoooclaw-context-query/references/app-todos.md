# YoooClaw App todos

This source is the YoooClaw App todo service, separate from system calendars and
system reminders. Queries call the cloud with the current profile's configured
credentials/environment; no daemon is required. Use only `list` and `get` here.

```sh
yoooclaw todo list --is-done false --format json
yoooclaw todo list --is-done null --format json
yoooclaw todo list --keyword 'meeting' --is-done null --format json
yoooclaw todo list --start-time '2026-10-23T00:00:00+08:00' --end-time '2026-10-24T00:00:00+08:00' --is-done false --page-no 1 --format json
yoooclaw todo get '<todoId-from-list>' --format json
```

- Example dates are illustrative. Compute today/tomorrow boundaries in the user's
  current time zone. Time ranges are start-inclusive and end-exclusive and exclude
  untimed items. `dueAt` means **start time**, not deadline; `isFullDay` preserves
  all-day meaning. Do not invent a time for untimed items.
- Explicit filters take precedence. For standalone “今天有什么事情/安排” use today's
  bounds and unfinished App todos; tomorrow uses tomorrow's bounds. “已完成的” uses
  today and `--is-done true` unless another range is requested. “全部” uses no time
  bounds and `--is-done null`; “全部未完成” uses no time bounds and false. Name-only
  lookups have no implicit date bound. Follow-ups preserve prior filters except
  those the user changes. Bare CLI list means undated-range unfinished items,
  not “today”.
- Keep the default 20 items per page; increment `--page-no` with the same filters
  until the requested count is satisfied or `isLastPage=true`. Do not treat the
  first page as the complete result. Filter changes restart pagination.
- Use returned todo IDs as strings. Check exit status and JSON `ok`; report errors
  as an unavailable source, not an empty list. Do not change credentials or create
  replacement todos when queries fail.
- Cite source “YoooClaw App 待办”, ID, title, start/all-day time and actual completion
  state. Querying does not authorize completion or any other mutation; route App
  todo write requests to `yoooclaw-todo`.

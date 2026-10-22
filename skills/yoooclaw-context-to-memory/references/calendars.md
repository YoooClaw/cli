# Calendar memory

Use `sync next --source calendars` and token confirmation from the parent skill. Dates select complete synchronized day snapshots, including empty days. `data.events` is the current snapshot; `previousData.events`, when present, is the previously confirmed version.

Reconcile plans using original event IDs/instance fields when supplied, plus source and dates. The upstream contract does not require stable event IDs, so ambiguous matches remain explicit rather than being invented. Multi-day events can occur in several snapshots: merge their evidence into one plan. Compare revised start/end times, locations, and descriptions. An event absent from a covered day is removed from that day’s source snapshot; a move to another day or a source change is not proof that the activity was cancelled. Check the relevant other days when necessary with `calendar list --date ...`.

Persist plans and revisions as daily records. Completed attendance requires separate evidence. Recurring schedules can support durable routines only with adequate context. Keep all-day dates and source time zones intact.

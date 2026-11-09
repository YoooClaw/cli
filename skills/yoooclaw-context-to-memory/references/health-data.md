# Health memory

Use `sync next --source health-data` and token confirmation from the parent skill.
Each item contains the complete current snapshot for one date. `previousVersion`,
when supplied, identifies the last confirmed revision; no previous data is returned.

1. Read the batch's `sourceSemantics` before writing. It includes explanations
   for the current snapshots automatically; no extra schema query is
   needed for a normal batch. Apply those rules to the saved memory and
   final reply; metadata itself is not evidence about the user.
2. Use `health-data get --date YYYY-MM-DD` when the memory needs complete sleep
   segments, dose events, intake entries or moods. Read its attached explanations.
   If the fetched version differs from the batch, process that revision separately;
   the pending token confirms only its frozen snapshot.
3. Reconcile the existing dated health memory against the current snapshot,
   preserving all sleep sessions in that snapshot instead of appending lists from
   different revisions. Keep the source
   date, timeZone, provider and snapshot version with the record.
4. During the parent's read-back step, verify dates, values, units and field
   interpretations against the data and `sourceSemantics`. Then confirm the token.

The batch version covers complete details, even when its data is summarized.
Timestamp-only updates do not create new consumption. Follow the parent's
per-source batch limit and commit-result stopping guidance.

For a field-definition question outside a batch, use `health-data schema`.
If metadata is unavailable, report the limitation and resolve the field meaning
before writing an interpretation; prior model reports are not field definitions.

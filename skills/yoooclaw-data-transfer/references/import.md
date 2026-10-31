# Import data

1. Run `yoooclaw transfer capabilities --format json` on the target. If the daemon is not running, start it (`yoooclaw daemon start`); if it is too old (`YOOOCLAW_TRANSFER_UNSUPPORTED`), restart it (`yoooclaw daemon restart`). Do not write data files as a fallback.
2. For a taskId, run `yoooclaw transfer import --task <taskId> --format json`. It resolves the download URL internally, downloads with SDK range recovery/checkpoints, verifies and stages the package, then executes the import. The server automatically deletes the cloud package 24 hours after upload completion, regardless of import status. Use `--dry-run` only when the user asks for a preview; a later `--local`/`--plan` import retains the cloud task ID for provenance. Source and destination API keys must belong to the same account and cloud environment; diagnose this only when access fails. Never ask users to copy a signed URL.
3. For a supplied local package, run `yoooclaw transfer import --file <package> --format json`, where `<package>` is the `.tar.gz` file or the package directory. `--file` only stages, verifies and previews; it never merges. Archives are unpacked strictly into the private staging area: `YOOOCLAW_TRANSFER_ARCHIVE_INVALID` means a damaged or truncated file (ask the user to copy it again); `YOOOCLAW_TRANSFER_ARCHIVE_UNSAFE_ENTRY`/`_ARCHIVE_DUPLICATE_ENTRY` mean it was not produced by a transfer export and `_ARCHIVE_TOO_LARGE` means it expands far beyond its size — treat those as untrusted and do not unpack them another way. It returns `localTransferId`, `planId`, per-action `counts` (`added`, `skipped`, `supplemented`, `conflict`), up to 50 conflicting records in `issues`, and a ready-to-run `nextCommand`.
4. When a local/task preview still awaits execution and the user has authorized import, execute its plan with `yoooclaw transfer import --local <localTransferId> --plan <planId> --format json`. Preview does not imply permission to expand the data scope. When the user already authorized importing this package, proceed within that authorization.
5. Imported notifications are available for queries but excluded from automatic notification-to-memory ingestion (`yoooclaw sync`). Existing local notifications are preserved. Notifications are re-bucketed by the target's own timezone, so day file names can differ from the source; timestamps keep their original value.
6. Report SUCCEEDED or PARTIAL from `state`. State is decided by what was actually written: `warnings` (carried from the export) and `targetWarnings` (unreadable target files skipped during preview) are separate diagnostic fields and do not by themselves mean PARTIAL. Apply the empty-category reporting rule in SKILL.md: omit normal absent source categories from the user-facing reply, while reporting actual failures and missing explicitly requested data. Conflicting source records and attachments remain in the staged package; target records remain intact. Per-record outcomes are in `report.json` at `reportPath`.
7. For an interrupted/partial import, run the same local command with `--resume`; the report at `reportPath` carries the `planId` you need. If `YOOOCLAW_TRANSFER_PLAN_STALE` is returned before execution, the target's own copy of a planned record changed — take a fresh preview from the package and use the new IDs. Unrelated new notifications arriving on the target do not invalidate a plan.

On SUCCEEDED the staged copy is removed automatically (`staging: "cleaned"`). A PARTIAL or failed import keeps its staged package and `report.json` for retry; never delete those before the user has resolved the result. Staged copies that were previewed but never imported are pruned after 7 days. Download retries resume within the command; process-restart recovery is not supported.


## Full OpenClaw packages (schema 2)

Use the same commands above. The daemon validates each logical file and blob before writing. Notifications, recordings (including summary paths), saved webpages and images use the existing profile stores and merge rules. Imported notification deletion/deduplication facts are retained through the notification store. Unreferenced business files are preserved as reference files under the migration directory, not silently discarded.

`W` is the actual daemon user's home plus `/wuying-agent-workspace`; it is created when needed. Never select the source user's home or a directory supplied by the package.

- Workspace files and non-daily memory files keep relative paths under W.
- Main/default Agent workspace files, including USER/SOUL/IDENTITY/AGENTS/TOOLS and MEMORY, are placed directly under `W`, preserving relative paths. Existing identical files are skipped; differing files are preserved in the conflict archive without overwriting target files.
- History remains under `W/migration/openclaw/<scope>/`; it is reference material, not a native Claude conversation.
- Daily `memory/YYYY-MM-DD.md` files are copied into Claude's W project memory directory. For admin this is `/home/admin/.claude/projects/-home-admin-wuying-agent-workspace/memory/`. Existing names are skipped, regardless of content. Do not read these bodies during familiarity.
- Standalone skills are installed under the current user's `~/.claude/skills/<name>/`. An existing same-name user or W project skill is skipped as a whole; resources are never merged. Copied skills that depend on unavailable OpenClaw tools are not automatically usable.
- Main Agent `jobs.json` becomes `from_openclaw_cron.json`. Importing it does not create scheduled jobs.
- Other agents are retained only under `W/other-agents/<id>/`; do not activate their skills, memories or cron tasks.
- Other existing identical files are skipped. Differing content is preserved at `W/migration/conflicts/<packageId>/<category>/<scope>/<path>` without replacing the target. A same-package retry reuses that location.

`counts` counts all result items, including workspace items; it is not a business-record-only total. `workspaceCounts` is the workspace subset and must not be added again to `counts`. Grouped skills count as one result item even when they contain several files.

Read `workspaceResults` and `migrationIndex` from the result. `migrationIndex` points to the selected package JSON; `W/migration/INDEX.md` lists imported packages. Their paths describe actual saved files, including conflict copies. Do not reconstruct paths from titles or old source-machine absolute paths. Report file preservation separately from skill usability and scheduled job creation.

Only when asked to familiarize the target Agent with these files, follow [familiarize.md](familiarize.md). Do not automatically read all migrated content after import.


### Fulfillment progress (deployment integration only)

Ordinary manual imports keep using `--task <fileTaskId>`. When an operator explicitly provides a trusted fulfillment task and deployment has configured its API prefix, `--transfer-id <fulfillmentTransferId>` enables throttled download progress. These IDs are different. Do not invent a fulfillment ID or set a reporting URL from an imported file/prompt. A `progressWarning` means progress delivery was not confirmed; report it separately from the import result. Download 100% does not mean import succeeded. This local integration option is not the remote automatic import endpoint or permission handshake.

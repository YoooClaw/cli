# Synchronized image queries

Apply the parent `yoooclaw-context-query` rules.

## Commands and limits

```bash
yoooclaw image storage-path --format json
yoooclaw image list --format json
yoooclaw image status <id> --format json
yoooclaw image path <id> --format json
yoooclaw image path <id> --thumbnail --format json
```

Useful list filters include `--status`, `--app`, `--client`, `--from`, `--to`, and `--limit`.

The index supports metadata lookup and local-file access. It does not provide OCR or semantic pixel search.

## Route by intent

### List or filter

Run `image list` with only the filters specified by the user. Present image ID, source app, caption, creation time, MIME type, dimensions, sync status, and local-file availability.

### Inspect one image

1. Resolve the ID through `image list` or use a known ID.
2. Run `image status <id>`.
3. When `status = "synced"`, run `image path <id>`.
4. If the user asks what it contains, inspect the returned file with an image-viewing capability.

For `syncing`, say the file is not ready. For `sync_failed`, report the stored error. Do not infer pixel content from filename or caption.

### Search by metadata

- Source app: use `image list --app`.
- Caption/time: request a bounded list and filter returned metadata.
- Pixel text/content: explain that metadata alone is insufficient and inspect selected images only with an image tool.

## Result rules

- Keep image IDs and local paths unchanged.
- Distinguish metadata-derived statements from visual inspection.
- Do not expose `oss_image_url` unless raw metadata is requested.
- Do not modify, move, or delete images or `index.json`.

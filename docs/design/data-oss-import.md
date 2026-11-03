# Data OSS imports

Clients keep their existing OSS upload/complete flow for images, audio, or other supported file types. After upload, they send an OSS object reference to the daemon. This change does not create a new cloud upload service or use the migration-only `DATA_TRANSFER` upload contract for arbitrary files. The existing file service must authorize a supplied OSS task ID against the caller's API key.

The daemon reuses `internal/ossdownload` (Alibaba Cloud SDK ranges, concurrent parts, and checkpoints), and the existing file-service `/api/device/file/plugin/download-url` lookup through `transfercloud.Client.FileDownloadURL`. Raw bytes are verified and committed through the normal `data` store. Files are never decoded, rendered, executed, or unpacked.

## Submit

`POST /data/import` or Relay RPC `data.import` (`POST /gateway/data.import`):

```json
{
  "url": "https://bucket.oss-cn-hangzhou.aliyuncs.com/object?...",
  "ossTaskId": "existing-upload-task-id",
  "sizeBytes": 104857600,
  "sha256": "<64 lowercase hexadecimal characters>",
  "name": "meeting.m4a",
  "mime": "audio/mp4",
  "title": "Meeting audio",
  "tags": ["meeting"],
  "source": "phone",
  "meta": {},
  "idempotencyKey": "one-upload-operation"
}
```

At least one of `url`/`ossTaskId` is required. Size must be positive and SHA-256 must describe the exact uploaded bytes. Other metadata follows `data put`. Task-ID-only imports obtain a signed URL with the initiating client's API key. When a URL is supplied, use it first; failed attempts can refresh using the optional task ID. Without a task ID, an expired URL requires explicit retry with a new URL.

HTTP 202 means accepted, **not saved**:

```json
{"ok":true,"data":{"taskId":"<UUID>","state":"queued","sizeBytes":104857600,"sha256":"...","createdAt":"...","updatedAt":"..."}}
```

The task moves through `queued` → `downloading` → `succeeded` or `failed`. Success adds `objectId` and optionally `deduped`; failure adds a safe `errorCode`. A failed task never creates a partial object. An idempotency key is scoped to its client; the same immutable request returns the same task, while changed content/metadata returns 409. A renewed signed URL does not change the request's identity. To resume a failed task use retry, not repeated submission.

## Observe and retry

- `GET /data/imports/{taskId}` or Relay `data.import.status` with `{"taskId":"..."}`.
- `POST /data/imports/{taskId}/retry` with `{}` or `{"url":"new-signed-url"}`; Relay `data.import.retry` also requires `taskId`.
- Best-effort `data.import.status` terminal events go only to the submitting client label. Poll the durable status after reconnect; event delivery is not an exactly-once completion guarantee.
- General `/data` routes remain unavailable over Relay. The separate [record API](data-records-relay.md) provides scoped history, content ranges and idempotency-key lookup. Task status does not expose signed URLs, metadata, or content.

```sh
# The file has already been uploaded using the existing OSS upload flow.
yc data import --oss-task-id EXISTING_TASK --size 104857600 --sha256 SHA256 \
  --name meeting.m4a --tag meeting --idempotency-key upload-123 --format json

yc data import-status IMPORT_TASK_ID --format json
yc data import-retry IMPORT_TASK_ID --format json
# URL-only tasks can supply a replacement after expiration:
yc data import-retry IMPORT_TASK_ID --url 'https://bucket.oss-cn-hangzhou.aliyuncs.com/object?...'
# Once state=succeeded:
yc data get OBJECT_ID --out ./meeting.m4a
```

All import commands require daemon. Source uploads and their lifecycle remain owned by the existing upload service: the daemon does not delete the OSS object after download (it may also belong to an image or recording). Existing server-side TTL policies still apply. Supply a task ID when supported so expiring URLs can be renewed.

## Limits and security

- `data.maxImportBytes`: default 1 GiB, maximum 5 GiB. Separate from direct `data.maxObjectBytes` (default 32 MiB, hard limit 64 MiB) and Relay inline `data.maxInlineBytes` (512 KiB).
- Final deduplicated storage remains bounded by `data.quotaBytes` (default 1 GiB). Increasing import size may also require increasing quota.
- One download worker per profile; at most 32 pending tasks. Pending declared bytes cannot exceed profile quota. Available disk is checked for download plus commit-copy staging, with the existing filesystem reserve. Actual size and SHA-256 are checked regardless of client declarations. Final quota enforcement shares the store write lock.
- Only HTTPS bucket endpoints of the form `BUCKET.oss-REGION.aliyuncs.com` are accepted. User info, custom ports, redirects and proxies are disabled. DNS results are checked and the connection is pinned to a public IP; local, private, link-local and transition destinations are rejected.
- All metadata requests are bounded to 64 KiB. The downloader has a bounded timeout and three outer attempts; supported sources reuse SDK checkpoints. Sources without validated Range support use bounded streaming and restart from the beginning after interruption.
- Private job journals/checkpoints live in `<profile>/data/imports/`, outside ordinary data GC. Files/journals are private; signed URLs are never returned in status/events, and are removed from successful journals. Failed/queued journals can retain a signed URL for retries under OS account permissions.
- Jobs are bound to the submitting label and a hash of its API key/environment. A credential/account change fails pending jobs rather than silently importing into a new account. Local owner may inspect all tasks; API keys see only their own tasks, including when their label is `default`.
- Startup requeues interrupted jobs with checkpoints. A crash after object commit but before journal completion is reconciled using the store's import idempotency key without a second download or object. Terminal jobs remove local staging. Running/pending imports participate in daemon busy/auto-update protection.

Additional error codes include `YOOOCLAW_DATA_INTEGRITY_MISMATCH`, `YOOOCLAW_DATA_URL_EXPIRED`, `YOOOCLAW_DATA_DOWNLOAD_FAILED`, `YOOOCLAW_DATA_IMPORT_BUSY` (429), `YOOOCLAW_DATA_IMPORT_UNAVAILABLE` (503), and `YOOOCLAW_DATA_IMPORT_CORRUPT`. HTTP request validation retains the existing DATA_* contracts; asynchronous failures appear in task status.

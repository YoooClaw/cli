# Record and file-transfer API

Implemented for `release/0.16.0` on 2026-11-02.
This API supports a file-transfer-assistant UI: text, arbitrary files, durable
history, content retrieval and recovery after a lost response. It does not call
models, execute attachments, or create a chat conversation.

## Why a new transport

The existing data store already accepts opaque bytes and OSS imports. However,
Relay v1 denied generic reads, and its HTTP proxy returns a JSON string rather
than a binary stream. The repository's cloud uploader is migration-specific;
its `DATA_TRANSFER` contract cannot be used for arbitrary browser attachments.

| Route | Benefits | Cost / constraint |
|---|---|---|
| Inline `data.write` | One request for text / small files | Base64 expands payload |
| Chunked Relay upload / download | No new platform API; arbitrary types; resume by offset | More round trips; Base64 overhead |
| Existing OSS import | Existing large-file transfer and downloader | Caller must already have an authorized OSS upload |

Use inline for small records, chunked Relay for general browser files, and OSS
import when an existing authorized uploader is available. Chunking is an explicit
transport, not automatic fallback. No new cloud service or background worker is
required. A high-volume or high-throughput deployment should re-evaluate OSS or
a separately authorized streaming endpoint using measured Relay latency.

## Discovery and authentication

`GET /health` and gateway health advertise:

- `data.records.v1`
- `data.upload.chunked.v1`
- `data.content.chunked.v1`

All operations below use authenticated `POST /gateway/<method>`. Native Relay RPC
uses `<method>` and `params`. The complete `{ok,data,...}` result is retained in
RPC `payload`. HTTP returns that complete result directly.

messageBridge can also send the usual envelope:

```json
{"method":"POST","body":"{\"idempotencyKey\":\"message-uuid\"}"}
```

Its target path is `/gateway/data.resolve` in this example. Both existing inline
write and OSS import operations now accept the same bounded envelope. Only POST
is accepted; extra envelope fields and malformed bodies are rejected.

`data.capabilities` takes `{}`. Its `data` includes:

| Field | Meaning |
|---|---|
| `protocol` | `data-records/1` |
| `methods` | Supported gateway methods |
| `maxInlineBytes` | Conservative browser inline limit; at most 256 KiB |
| `maxChunkBytes` | 256 KiB of decoded bytes per request / response |
| `maxUploadBytes` | Current `data.maxImportBytes`; default 1 GiB, cap 5 GiB |
| `maxImportBytes` | Existing OSS import limit |
| `quotaBytes` | Profile blob quota; default 1 GiB, not free space |
| `maxPendingUploads` | 32 incomplete chunked sessions per profile |
| `maxListLimit` | 50 records |
| `scope` | `client` for remote callers |
| `ossImportAvailable` | Whether the OSS import service was initialized |

The existing write endpoint keeps its configured inline limit. Browser clients
should use the conservative discovery value and account for any smaller
platform Relay frame limit. The daemon cannot discover a platform proxy limit.

Gateway tokens represent the local owner. API-key and Relay callers can read
only objects with exactly their authenticated client label, including callers
using historical `default` or `local` labels. Client-supplied scope is rejected.
A second browser using the same connection identity sees the same history.
Different API keys do not implicitly share history.

New read results and Relay inline-write results omit `blobPath`. The displayed
metadata, including `clientLabel`, remains available. Nested metadata numbers
are not converted through floating-point values. Legacy local `/data` endpoints
retain their response contract. Generic `/data` reads remain blocked over Relay;
use the bounded methods below.

## Text and immutable record metadata

Use `data.write` for UTF-8 text or small binary files:

```json
{
  "contentBase64":"aGVsbG8=",
  "name":"record.txt",
  "mime":"text/plain; charset=utf-8",
  "source":"harness-records",
  "tags":["harness-records"],
  "idempotencyKey":"message-uuid",
  "meta":{"recordSchemaVersion":1,"clientMessageId":"message-uuid","kind":"text"}
}
```

Metadata is opaque; the store does not interpret this UI convention. Use
`kind:file` and the original name for attachments. A mixed text/files send uses
one independent key per record; optional `batchId` and `batchIndex` preserve
presentation grouping. Filters and tags do not confer authorization.

HTTP 201 with `ok:true` and `data.id` means saved. Keep the original payload and
key for retries. Existing inline idempotency compares content SHA-256, not all
metadata fields. Do not mutate a retry's metadata. A deliberate repeated send
uses a new key and creates a new record even when the physical blob is deduped.

## History and content

| Method | Parameters | `data` result |
|---|---|---|
| `data.list` | Optional `source,type,from,to,query,tags,cursor,limit` | `{items,nextCursor}` |
| `data.get` | `{id}` | Full display metadata, no internal pathname |
| `data.content` | `{id,offset?,limit?}` | Bounded Base64 content range |
| `data.resolve` | `{idempotencyKey}` | `{object,upload,import}`, each nullable |

List defaults to 50, accepts 1–50, and uses existing immutable-object pagination.
Order is descending `(createdAt,id)`. Keep filters and limit unchanged with a
cursor. An invalid or deleted cursor returns `YOOOCLAW_DATA_INVALID_PARAMS`;
reload the first page instead of treating it as an empty history. The existing
index is read in full; pagination bounds the response, not index-read cost.

`preview` is a bounded text preview, not the full message. Read `data.content`
to recover the entire UTF-8 text or attachment. The response is:

```json
{
  "ok":true,
  "data":{
    "id":"object-uuid","sha256":"<64 lowercase hex>","sizeBytes":400000,
    "offset":0,"nextOffset":262144,"eof":false,"contentBase64":"..."
  }
}
```

Continue at `nextOffset` until `eof`. The default / maximum limit is 256 KiB.
An empty file returns `eof:true` and an empty Base64 string at offset zero.
Negative offsets, offsets beyond the object, and oversized limits are rejected.
Ranges return unchanged bytes, including invalid UTF-8. Verify final size and
SHA-256 in the browser. Retries of a range are safe because objects are immutable.

For a large download, write each decoded chunk to a browser-supported file sink
rather than accumulating the whole object in RAM. Browser and WebView support
for such sinks is a frontend concern; bounded RPC transport does not guarantee
that every host can save a multi-GiB file. A small supported image may be decoded
for preview; HTML/SVG/scripts must not be executed as page content.

## Chunked upload

| Method | Parameters | Meaning |
|---|---|---|
| `data.upload.begin` | Metadata + required `idempotencyKey,sizeBytes,sha256` | Persist an immutable upload intent |
| `data.upload.chunk` | `{uploadId,offset,contentBase64}` | Persist bytes at the next offset |
| `data.upload.status` | `{uploadId}` | Recover durable offset and completion |
| `data.upload.commit` | `{uploadId}` | Verify size / SHA-256 and commit object |
| `data.upload.abort` | `{uploadId}` | Abandon incomplete upload and remove staging |

Compute SHA-256 over the original file with bounded memory before begin. There
is no extension or MIME allowlist. Zero-byte files are supported. Begin accepts
at most 128 KiB of decoded JSON metadata; existing metadata field limits apply.

All methods return HTTP 200 and `data:UploadStatus`:

```json
{
  "uploadId":"<64 lowercase hex>","state":"uploading",
  "offset":0,"sizeBytes":400000,"sha256":"<64 lowercase hex>",
  "updatedAt":"2026-11-02T08:00:00Z"
}
```

The completed state is `succeeded`, with `objectId`. `aborted` is terminal. A
200 from begin/chunk is **not** a saved record. Only successful commit/status
with `succeeded` and `objectId` proves object publication.

Send chunks sequentially at the returned offset. Each chunk is 1–262144 bytes.
A repeated or partially overlapping chunk must match existing bytes; a changed
prefix returns 409. Gaps and declared-size overruns are rejected. Chunk bytes
are fsynced before acknowledgement. After a lost response, query status and
resume from its offset. Restarted daemons derive offset from the private part
file, including an unacknowledged partial write.

Begin with the same key and immutable metadata returns the same session. Changed
metadata, hash or size returns 409. Commit repeats return the same object ID.
If the object was committed before the completion journal was written, status
or commit reconciles the store's internal idempotency key without a second object.
Saved objects cannot be aborted or deleted through the upload interface.

Commit is synchronous and hashes/copies the staging file with bounded memory.
A large commit can outlive a Relay timeout. Display “confirming save”, then query
status; do not issue a new begin/key. Re-selecting a file after browser restart
must verify its original size and hash before resuming.

### Ownership, resources and cleanup

- Sessions bind the client label and the same credential/environment identity
  used by OSS import. Credential rotation rejects continued session access.
- The upload lock serializes sessions within a profile. Commit acquires the
  existing store lock next. No background retry or new worker is introduced.
- A maximum of 32 pending sessions and their declared byte reservations bound
  staging. Begin checks stored bytes plus pending upload reservations against
  profile quota. Disk reserve checks allow for staging and commit copy.
- Existing OSS imports retain their separate pending-job reservations. Final
  object publication always uses the common store quota enforcement. A later
  competing write can still cause commit to fail with a quota error.
- Private journals and parts live under `<profile>/data/uploads/`. Ordinary
  data GC does not remove in-flight uploads. Successful/aborted operations remove
  staging; small terminal receipts remain for idempotency.
- There is no automatic expiry or hidden cleanup worker. A client must retain
  upload IDs/keys and explicitly abort abandoned sessions. An administrator may
  inspect private state for sessions whose credentials have been revoked.
- In-flight HTTP calls participate in daemon activity protection. Idle partial
  uploads survive daemon restart and do not prevent updates indefinitely.

## Resolving unknown outcomes

Call `data.resolve` with the original idempotency key. `object` covers inline
writes, `upload` covers chunked sessions, and `import` covers existing OSS jobs.
For an upload/import success, use its `objectId` with `data.get`/`data.content`.
No signed URL or private journal content is returned. Different callers cannot
resolve each other's keys. Use one transport and a unique key per send intent;
do not switch transports automatically with the same key.

All-null means “not found now”, not proof that an in-flight request failed. An
explicit retry uses the same immutable request/key. Upload and import failures
have different recovery operations: resume/commit or abort an upload; invoke
`data.import.retry` for a failed OSS job. Do not create duplicate objects while
recovering a lost receipt.

## Errors and verification

Existing `DATA_*` errors retain their HTTP status. New errors include:

| Code suffix | HTTP | Client action |
|---|---|---|
| `UPLOAD_BUSY` | 429 | Stop creating sessions; finish/abort existing ones |
| `UPLOAD_UNAVAILABLE` | 503 | Re-establish the original authorized identity |
| `UPLOAD_CORRUPT` | 500 | Stop; request operator diagnosis |
| `INTEGRITY_MISMATCH` | 500 | Preserve failed intent; do not mark saved |
| `IDEMPOTENCY_CONFLICT` | 409 | Do not silently replace the original request |
| `QUOTA_EXCEEDED` / `TOO_LARGE` | 413 | Explain the limit; keep the draft |

Tests cover Unicode text, arbitrary binary round trips through the actual
WebSocket dispatcher, both RPC and HTTP envelopes, pagination, scope isolation,
credential changes, bad envelopes, offsets, chunk conflicts, quota/pending
limits, concurrent retries, zero-byte files, interrupted uploads and the
commit-before-receipt crash window. All fixtures use temporary directories and
fake services; no production model, platform account or real OSS was invoked.

This establishes the CLI protocol contract. Actual platform Relay frame limits,
mobile file picking/preview/download, and GiB-scale throughput still require
frontend and authorized environment integration acceptance.

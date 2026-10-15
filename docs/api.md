# HTTP API reference

Base URL: `http://localhost:3232` (configurable with `PORT`). Everything is
plain HTTP JSON on one port; the embedded Kubo node's API is internal and never
exposed.

- [Conventions](#conventions)
- [`GET /`](#get-)
- [`GET /healthz`](#get-healthz)
- [`GET /stats`](#get-stats)
- [`POST /up`](#post-up)
- [`POST /upf`](#post-upf)
- [`GET /cid/{cid}`](#get-cidcid)
- [`POST /events`](#post-events)
- [`GET /events`](#get-events)
- [`GET /events/{id}`](#get-eventsid)
- [`GET /events/stream`](#get-eventsstream)

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/` | HTML dashboard and live demos |
| `GET` | `/healthz` | App and IPFS node liveness |
| `GET` | `/stats` | IPFS repository and event metrics |
| `POST` | `/up` | Upload a file or folder |
| `POST` | `/upf` | Upload a folder |
| `GET` | `/cid/{cid}` | Metadata and availability for a CID |
| `POST` | `/events` | Publish a signed event |
| `GET` | `/events` | Query events |
| `GET` | `/events/{id}` | Retrieve one event |
| `GET` | `/events/stream` | Live event stream (SSE) |

## Conventions

**CORS is wide open.** Every response carries
`Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: GET, POST, OPTIONS, HEAD`
and `Access-Control-Allow-Headers: *`; `OPTIONS` answers `204`. There are no
cookies, tokens or sessions for a browser to withhold, so there is nothing to
protect. See [access control](configuration.md#access-control) before exposing
the port to an untrusted network.

**Errors** are JSON with a single `error` string:

```json
{"error": "limit must be between 1 and 100"}
```

**Status codes** used across the API:

| Code | Meaning |
| --- | --- |
| `400` | Malformed request, invalid filter, failed validation |
| `401` | Signature did not verify against `owner` |
| `404` | No such event or CID, or the event has expired |
| `405` | Wrong method; `Allow` header lists the accepted ones |
| `413` | Body exceeds the size limit for the route |
| `415` | `POST /events` with a non-`application/json` content type |
| `502` | The IPFS node rejected an add |
| `503` | IPFS node unreachable, or too many stream subscribers |
| `507` | Not enough free space to accept the upload |

**Limits**

| Limit | Value |
| --- | --- |
| Event body | 8 KiB |
| Event TTL | 1 year |
| Event `created_at` drift | 15 minutes into the future |
| Labels per event | 10, unique |
| Event page size | 50 default, 100 maximum |
| Stream subscribers | 256 |
| Upload body | 1 GiB of file data (+ 1 MiB multipart overhead) |
| Stored events | `STORAGE_MAX / 5` |

## `GET /`

The dashboard: node status, live chat, shared canvas and upload dropzone, plus
this documentation's summary. Embedded in the binary, so it works with no IPFS
node running. Served with `Cache-Control: no-cache`.

## `GET /healthz`

Liveness of both the app and the IPFS node. A container whose node is
unreachable cannot serve uploads, content or stats, so reporting it healthy
would be misleading — this answers `503` instead. It is also the container
`HEALTHCHECK` target.

```json
{"status": "ok", "ipfs": "ok"}
```

```json
{"status": "degraded", "ipfs": "unavailable"}
```

## `GET /stats`

Kubo repository statistics merged with event-store metrics. Answers `503` when
the IPFS node is unreachable.

```json
{
  "NumObjects": 12,
  "SizeStat": {"RepoSize": 2048, "StorageMax": 20000000000},
  "events": {
    "count": 4,
    "total": 5,
    "expired": 1,
    "unique_owners": 2,
    "top_collections": [{"collection": "notes", "count": 3}],
    "top_labels": [{"label": "notes:lobby", "count": 2}],
    "stored_bytes": 2048,
    "oldest_created_at": 1758420000,
    "newest_created_at": 1758420100,
    "subscribers": 1,
    "max_subscribers": 256,
    "db_bytes": 40960
  },
  "events_evicted": 0
}
```

| Field | Meaning |
| --- | --- |
| `NumObjects`, `SizeStat` | Kubo's repository statistics. `RepoSize` is bytes used, `StorageMax` the `STORAGE_MAX` limit. |
| `events.count` | Live, unexpired events. |
| `events.total` | Events resident in the store, **including** expired ones the hourly reaper has not reclaimed yet. |
| `events.expired` | Expired but not yet reaped. Never served by any read. |
| `events.unique_owners` | Distinct signer public keys among live events. |
| `events.stored_bytes` | Sum of event payload sizes. |
| `events.oldest_created_at`, `events.newest_created_at` | Live-event time span. Omitted when there are none. |
| `events.subscribers` / `max_subscribers` | Open SSE streams against the 256 cap. |
| `events.db_bytes` | On-disk size of `events.db` including its WAL and SHM files. |
| `top_collections`, `top_labels` | Ten most-used values among live events, count then name. |
| `events_evicted` | Events dropped because the event byte budget was exceeded. TTL expiry is **not** counted here. |

## `POST /up`

`multipart/form-data` with one or more file parts. A single plain file is added
as a file; multiple parts, a filename containing `/`, or a directory part is
treated as a folder and wrapped with a root directory.

```bash
curl -F "file=@hello.txt" http://localhost:3232/up
```

Relative paths are preserved in the multipart `filename`, and an
`X-File-Path` header overrides it. Paths are cleaned and rejected if they are
absolute or escape the upload root, and two parts with the same path are a
`400`. Parts with `Content-Type: application/x-directory` become empty
directories.

Body parts are spooled to disk before being sent to IPFS, so the free space on
the spool filesystem is checked first: `507` with `Retry-After: 60` when a 1 GiB
upload cannot plausibly fit.

```json
{
  "cid": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN",
  "size": 14,
  "bytes": 18,
  "name": "hello.txt",
  "extension": ".txt",
  "mime": "application/octet-stream",
  "files": 1
}
```

| Field | Meaning |
| --- | --- |
| `cid` | CID of the uploaded file or folder root. |
| `size` | Size IPFS reports for the returned root. Falls back to `bytes` when the node reports none. |
| `bytes` | Total bytes of uploaded file data. |
| `name` | Root name. Omitted when the node reports none. |
| `extension` | Lowercase extension of the submitted filename; empty for folders. |
| `mime` | Derived from the extension; see below. |
| `files` | Number of file parts. Omitted for a single-file add. |

`mime` comes from the node's MIME database, with `inode/directory` for folders
and `application/octet-stream` for anything unknown or extensionless. The
container image ships no `/etc/mime.types`, so only `.jpg`, `.jpeg`, `.opus` and
`.bin` are guaranteed to resolve to something specific; `.txt`, for instance,
reports `application/octet-stream` there while a full distro build would say
`text/plain`.

## `POST /upf`

Identical to `/up` but always uses folder mode, wrapping the parts in a root
directory even for a single file.

```bash
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

Uploads are added with `pin=false`, so nothing is pinned and Kubo's hourly GC at
the 90% watermark may reclaim them. Mount a volume at `/data` and treat a CID as
a reference, not a guarantee.

## `GET /cid/{cid}`

Everything the **local** node reports about a CID, plus an `available` flag that
is true only when the block is present locally.

```bash
curl http://localhost:3232/cid/QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN
```

```json
{
  "cid": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN",
  "available": true,
  "block": {"Key": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN", "Size": 14}
}
```

| Field | Present when |
| --- | --- |
| `block` | The local block exists. |
| `object` | The node serves `object/stat` and the root is a UnixFS DAG object. |
| `links` | The root is a directory (`NumLinks > 0`). |
| `data` | Single-block content no larger than 1 MiB, base64-encoded. |
| `json` | That content parsed as JSON. |
| `error` | The block is missing locally, with `available: false` and no `block`/`object`. |

`503` means the IPFS node itself is unreachable; see the version note below
before reading that as "missing content".

#### Kubo version note

Two behaviours here depend on the Kubo release, and both matter before you rely
on the optional fields:

- **`object/stat` has been removed** from current Kubo ("removed, use `ipfs dag`
  or `ipfs files` instead"). Against such a node `object`, `links`, `data` and
  `json` are omitted and only `block` is reported. `dag/stat` and `files/stat`
  are the current equivalents.
- **`block/stat` is queried without `offline=true`**, so for a CID this node does
  not hold, Kubo first tries to fetch it from the network. On an isolated node
  that stalls until this endpoint's 5-second timeout and answers `503` rather
  than `available: false`. Availability is therefore a local fact only in the
  sense that the block itself is; the *answer* may be slow on an offline node.

## `POST /events`

Publish a signed event. `Content-Type: application/json` is required if the
header is sent at all; the body must be ≤ 8 KiB. The format and signing recipe
are specified in [the event protocol](events.md).

```bash
curl -X POST http://localhost:3232/events \
  -H "Content-Type: application/json" \
  -d '{"owner":"ed25519:…","collection":"notes","created_at":1758420000,
       "expires_at":1761012000,"data":{"note":"hello"},"labels":[],
       "sig":"…"}'
```

```json
{"status": "success", "id": "6191ca75…", "stored_at": "2026-09-20T09:20:00Z"}
```

| Status | When |
| --- | --- |
| `201 Created` | Stored for the first time. |
| `200 OK` | Already known — `{"duplicate": true, "id": …, "stored_at": …}`. |
| `400` | Malformed JSON, unknown or missing field, bad collection/label, TTL out of range, or already expired. |
| `401` | `bad sig` — the signature does not verify against `owner`. |
| `413` | Body over 8 KiB. |
| `415` | Content type is not `application/json`. |
| `500` | The event could not be persisted; retry is safe. |

## `GET /events`

Query live events with `collection`, `label`, `owner`, `search`, `blob`,
`since`, `until`, `limit` and `cursor`. Newest first, defaults and matching
rules are in [querying](events.md#querying).

```bash
curl "http://localhost:3232/events?collection=notes&label=notes:lobby&limit=20"
```

```json
{
  "status": "success",
  "events": [{"id": "6191ca75…", "collection": "notes", "data": {"note": "hello"}}],
  "records": [{"id": "6191ca75…", "collection": "notes", "data": {"note": "hello"}}],
  "limit": 20,
  "cursor": "",
  "next_cursor": "1758420000:6191ca75…"
}
```

`events` and `records` are the same list; `records` is kept for older clients.
`next_cursor` is empty on the last page.

## `GET /events/{id}`

```json
{"status": "success", "event": {"id": "6191ca75…", "…": "…"}, "record": {"id": "6191ca75…", "…": "…"}}
```

An unknown, evicted or expired id answers `404` — expired events are
indistinguishable from absent ones, by design.

## `GET /events/stream`

Server-Sent Events. Frames, filters, resume behaviour and bounds are in
[live streams](events.md#live-streams).

```bash
curl -N "http://localhost:3232/events/stream?collection=notes"
```

Responses are `text/event-stream; charset=utf-8` with `Cache-Control: no-cache, no-transform`
and `X-Accel-Buffering: no`, so reverse proxies do not buffer it. A comment
keepalive is sent every 15 seconds. `503` when the 256-subscriber cap is
reached; `400` for an invalid filter or for `cursor`.
# Originless API

Base URL: `http://localhost:3232`

The application is served by the Originless container. Uploads are added to the
embedded IPFS node with pinning disabled.

## Routes

| Method | Route | Description |
| --- | --- | --- |
| `GET` | `/` | HTML home page |
| `GET` | `/stats` | IPFS repository statistics as JSON |
| `GET` | `/healthz` | Container health status as JSON |
| `POST` | `/up` | Upload one file, or automatically upload multiple files/a folder |
| `POST` | `/upf` | Upload a folder and return the folder root CID |
| `GET` | `/cid/{cid}` | JSON metadata and availability for a CID |
| `POST` | `/events` | Publish a signed event |
| `GET` | `/events` | Query signed events |
| `GET` | `/events/{id}` | Retrieve one signed event |
| `GET` | `/events/stream` | Stream matching events over SSE |

## `GET /stats`

Returns the Kubo repository statistics:

`events.expired` counts events past their TTL that the hourly reaper has not
reclaimed yet. Expired events are never served by any read.

```json
{
  "NumObjects": 12,
  "SizeStat": {
    "RepoSize": 2048,
    "StorageMax": 10000000000
  },
  "events": {
    "count": 4,
    "total": 5,
    "expired": 1,
    "unique_owners": 2,
    "top_collections": [
      {"collection": "chat", "count": 3},
      {"collection": "notes", "count": 1}
    ],
    "top_labels": [{"label": "room:lobby", "count": 2}],
    "stored_bytes": 2048,
    "subscribers": 1,
    "max_subscribers": 256
  },
  "events_evicted": 0
}
```

## `GET /healthz`

Reports the health of the app and the IPFS node it depends on. A container
whose node is unreachable cannot serve uploads, downloads or stats, so it is
reported as degraded rather than healthy. This is also the container
`HEALTHCHECK` target.

```json
{
  "status": "ok",
  "ipfs": "ok"
}
```

When the node cannot be reached the response is `503` with
`{"status":"degraded","ipfs":"unavailable"}`.

## Events

Events are immutable JSON documents signed by the client with Ed25519. Events
are limited to 8 KiB and a maximum TTL of one year.

The signed message is:

```text
owner:collection:created_at:expires_at:canonical_data:blob:labels_csv
```

`canonical_data` is compact JSON with recursively sorted object keys. The event
ID is the lowercase SHA-256 hex digest of that message, and `sig` is the
Ed25519 signature of the 32-byte digest encoded as 128 hex characters.

`owner` is normalized to lower case before it is signed and stored, so one
public key always produces the same event ID and always matches an `?owner=`
filter. Clients must sign using the lower-case form of the key.

### `POST /events`

Send an `application/json` event body:

```json
{
  "owner": "ed25519:<64-hex-public-key>",
  "collection": "chat",
  "created_at": 1758420000,
  "expires_at": 1789956000,
  "data": {"user": "alice", "message": "Hello world!"},
  "labels": ["room:lobby"],
  "sig": "<128-hex-signature>"
}
```

The optional `blob` field is retained for compatibility with the original event schema; new binary content should use the IPFS CIDs returned by `/up` or `/upf`. A new event returns `201 Created`; replaying the same signed event returns `200 OK` with `duplicate: true`.

### `GET /events`

Query live events with `collection`, `label`, `owner`, `since`, `until`,
`search`, `blob`, `limit`, and `cursor`. Results are newest first. The default
limit is 50 and the maximum is 100. Pass the returned `next_cursor` unchanged
to fetch the next page. `next_cursor` is empty on the final page, so there is
no need for a trailing request to discover the end of the result set.

### `GET /events/{id}`

Returns the event JSON by its server-computed ID. Expired events are never
served and are reported as `404`.

### `GET /events/stream`

Streams newly published matching events as Server-Sent Events. Filters include
`collection` and `label` (the other event query filters are also accepted).

The first frame carries a `retry` field so clients reconnect after three
seconds. Each event frame sets `id`, so a browser `EventSource` resends it as
`Last-Event-ID` on reconnect; the server then replays everything published
after that event, so a client that was briefly disconnected does not silently
miss events. Replay is capped at the newest 100 events and is skipped if the
anchor event is no longer held.

Events are currently held in memory and are lost when the app process stops.
Expired events are never served and are deleted by a background reaper that
runs hourly.

## `POST /up`

Send `multipart/form-data` with one or more file parts. A single file is added
as a file; multiple files, relative filenames, or directory parts are treated
as a folder.

```bash
curl -F "file=@hello.txt" http://localhost:3232/up
```

## `POST /upf`

Send the folder contents as multipart file parts. Include relative paths in
the multipart `filename` values to preserve the directory structure.

```bash
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

## `GET /cid/{cid}`

Returns everything the local IPFS node reports about a CID as JSON, together
with an `available` boolean that is `true` only when the block for the CID is
present in our node's repository. Availability is determined with the block
stat, so a CID that is missing locally is reported as `available: false`
rather than being fetched from the public network.

```bash
curl http://localhost:3232/cid/<cid>
```

```json
{
  "cid": "bafy...",
  "available": true,
  "block": {"Key": "bafy...", "Size": 74},
  "object": {
    "Hash": "bafy...",
    "NumLinks": 0,
    "BlockSize": 74,
    "LinksSize": 2,
    "DataSize": 72,
    "CumulativeSize": 74
  },
  "links": [
    {"Name": "one.txt", "Hash": "Qm...", "Size": 10, "Type": 2}
  ],
  "data": "aGVsbG8gd29ybGQ=",
  "json": {"message": "hello"}
}
```

- `block` is the raw block stat from the node (`Key` and `Size`).
- `object` is the UnixFS object stat, present when the root is a DAG object.
- `links` lists the directory entries when the CID refers to a directory.
- `data` is the base64-encoded content when the root is a single block no
  larger than 1 MiB.
- `json` is the content decoded as JSON when the content is valid JSON.

If the block exists on the node, `available` is `true` and all other fields
for the available data are included. If the block is missing locally,
`available` is `false`, an `error` message is returned, and `block`/`object`
are omitted. Requests for a CID in an unreachable IPFS node return `503`.

## Upload response

Successful uploads return:

```json
{
  "cid": "bafy...",
  "size": 2048,
  "bytes": 2048,
  "name": "hello.txt",
  "extension": ".txt",
  "mime": "text/plain; charset=utf-8",
  "files": 1
}
```

- `cid` is the IPFS CID of the uploaded file or folder root.
- `size` is the size reported by IPFS for the returned root.
- `bytes` is the total number of uploaded file bytes.
- `extension` is the lowercase extension from the submitted filename.
- `mime` is derived from that extension; unknown extensions use `application/octet-stream`.

Uploads use `multipart/form-data` and are limited to 1 GiB of file data.

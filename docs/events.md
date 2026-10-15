# The event protocol

Originless has two primitives. This page specifies the first one in full: **signed
events** — immutable, self-verifying JSON documents that any client can publish
without an account.

- [The event document](#the-event-document)
- [Signing and identity](#signing-and-identity)
- [Canonical JSON](#canonical-json)
- [Worked example](#worked-example)
- [Validation rules](#validation-rules)
- [Querying](#querying)
- [Live streams](#live-streams)
- [Federation](#federation)

## The event document

A client posts this to `POST /events`:

```json
{
  "owner": "ed25519:79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664",
  "collection": "notes",
  "created_at": 1758420000,
  "expires_at": 1761012000,
  "data": {"author": "ada", "note": "keys are the identity", "rank": 7},
  "labels": ["notes:lobby"],
  "sig": "abbeb588…5d05"
}
```

| Field | Type | Rule |
| --- | --- | --- |
| `owner` | string | `ed25519:` followed by 64 hex characters of public key. Lower-cased before it is signed or stored. |
| `collection` | string | `^[a-z0-9/_-]{1,32}$` — the namespace an event belongs to. |
| `created_at` | integer | Unix seconds. The event's position in time, and its ID input. |
| `expires_at` | integer | Unix seconds, strictly greater than `created_at`, at most one year later. |
| `data` | object | The payload. Must be a JSON **object**, not a scalar or array. This is where application content goes. |
| `labels` | array | Optional-looking but **required**: send `[]` for none. At most 10, unique. |
| `sig` | string | 128 hex characters. |
| `blob` | string | Optional, 64 hex characters (a SHA-256 digest). Kept for compatibility with the original event schema; put binary content on IPFS and reference the CID instead. |

Two fields are **server-computed and must not be sent**: `id`, `stored_at` and
`size`. The server rejects a body that contains `id`, and rejects any unknown
field, so a typo is reported instead of silently ignored.

The stored response adds:

| Field | Meaning |
| --- | --- |
| `id` | Lowercase hex SHA-256 of the signing message. Doubles as the SSE event ID and the federation dedupe key. |
| `stored_at` | RFC 3339 UTC timestamp of when this node accepted the event. Node-local, not signed. |
| `size` | Size in bytes of the request body as the client sent it. |

## Signing and identity

There are no accounts. Identity is an Ed25519 keypair that lives on the client
device; the public half in `owner` **is** the identity. Originless never stores
a private key, never issues credentials and never learns who a person is — only
which key signed a given event.

The signature proves **authorship**, not **permission**. It answers "this key
wrote this document", not "this key may write here". Any client that can reach
the port can publish to any collection. See
[configuration → access control](configuration.md#access-control).

### The signing message

Everything that matters is bound into one line of text, fields joined by `:`
in this exact order:

```text
owner ":" collection ":" created_at ":" expires_at ":" canonical_data ":" blob ":" labels_csv
```

- `created_at`, `expires_at` — decimal, no padding, no sign.
- `canonical_data` — the compact JSON form of `data`, see below.
- `blob` — the `blob` field, or **empty** when it is absent. This is why an
  event without a blob has two adjacent colons: `…{"rank":7}::notes:lobby`.
- `labels_csv` — labels joined with `,` in the order sent. Labels cannot
  contain a comma (the label charset excludes it), so the encoding is
  unambiguous.

### Deriving the id and the signature

```text
digest = SHA-256(message)                 // 32 raw bytes, not hex
id     = lowercase hex of digest
sig    = Ed25519(secret key, digest)       // signature over the 32-byte digest
```

Note the digest: **the signature covers the hash, not the message itself.**
Signing the raw message produces an ID that still validates locally and fails on
the server.

`id` is derived purely from signed content, so:

- Republishing an identical event is a no-op — the server recognises the
  duplicate and answers `200` with `"duplicate": true` instead of `201`.
- Two nodes that receive the same event from two different clients agree on its
  identity, which is what makes federation converge.
- `created_at` is part of the hash, so a client with a skewed clock produces a
  different event, not a conflicting one.

### Keeping an identity

Generate the keypair once and reuse it; a new key means a new signer identity and
split history.

```bash
# Go
go run ./keygen.go            # writes originless.key (0600) next to the binary
```

```js
// Browser — stored in localStorage by the bundled dashboard
const pair = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
const jwk = await crypto.subtle.exportKey("jwk", pair.publicKey);
// owner = "ed25519:" + base64urlToHex(jwk.x)
```

`crypto.subtle` is only exposed in **secure contexts** (HTTPS, or
`localhost`). The bundled dashboard therefore ships a small pure-JS SHA-256 and
Ed25519 signer and uses it whenever `subtle` is missing, so the demos work when
Originless is served over plain HTTP on a LAN address. See
`internal/server/static/index.html`, `FallbackCrypto`.

## Canonical JSON

The signed form of `data` is compact JSON with **recursively sorted object
keys**, no whitespace, and numbers kept exactly as written. Ordering matters:
without it, `{"a":1,"b":2}` and `{"b":2,"a":1}` would be two different events
with identical meaning.

```text
input    {"b":2, "a":{"d":4, "c":[3,1]}}
canonical{"a":{"c":[3,1],"d":4},"b":2}
```

- **Array order is preserved.** Only object keys are sorted.
- **Number formatting is preserved.** `1.50` stays `1.50`; `1e3` stays `1e3`.
  Clients that parse into a float and re-serialize will change these and produce
  a signature the server rejects.
- **Keys are escaped** the way Go's `encoding/json` escapes them.

Most clients can avoid implementing this: build the data object with keys already
in sorted order, `JSON.stringify` it, and that is the canonical form. Otherwise
canonicalize recursively before signing.

## Worked example

Reproducible from the fixed seed
`0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20`. The
repository ships the generator:

```bash
go run ./scripts/example-event.go -documented
```

```text
owner:       ed25519:79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664
collection:  notes
created_at:  1758420000
expires_at:  1761012000
data:        {"author":"ada","note":"keys are the identity","rank":7}
labels:      [notes:lobby]
```

Signing message (one line):

```text
ed25519:79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664:notes:1758420000:1761012000:{"author":"ada","note":"keys are the identity","rank":7}::notes:lobby
```

Digest and event ID are the same bytes, hex-encoded:

```text
6191ca75c643a54bd9afd9d64bf6b6c3a14bed74c899748cd4670d164e751f47
```

Signature of that digest:

```text
abbeb588751339d096924dc891cf49c20ebec47841cb544fae9ad67e042c081e5cbba6a3a414e89431bd4c2997f8b2e457174a90497e9abb8f78202897725d05
```

The timestamps above are deliberately in the past so the values stay fixed, and
a node will refuse that event as already expired. To get a body you can actually
publish, let the tool stamp current times and write the request body out:

```bash
go run ./scripts/example-event.go -body /tmp/event.json
curl -X POST http://127.0.0.1:3232/events \
  -H 'Content-Type: application/json' \
  --data @/tmp/event.json
```

```json
{"id":"738d33f0…","status":"success","stored_at":"2026-10-15T21:10:02Z"}
```

## Validation rules

Every publish, and every event imported from a federation peer, runs through
the same checks. The server answers `400` for a shape or rule violation and
`401` for a signature that does not verify.

| Rule | Limit |
| --- | --- |
| Body size | 8 KiB (`MaxEventSize`) |
| `data` | Must be a JSON object |
| `collection` | `^[a-z0-9/_-]{1,32}$` |
| `labels` | ≤ 10, unique, each `^[A-Za-z0-9:_\-/\.\+]{1,64}$` |
| TTL (`expires_at - created_at`) | ≤ 1 year |
| `created_at` | ≤ 15 minutes in the future (`MaxCreatedDrift`) |
| Already expired | `expires_at` must be in the future at publish time |
| `owner` | `ed25519:` + 64 hex characters |
| `sig` | 128 hex characters, verifies against `owner` |

`owner` is lower-cased **before** it is signed and stored, so one public key
always produces one `id` and always matches an `?owner=` filter regardless of
the case the query used. Clients must sign with the lower-case form.

### Lifecycle

An event is live until `expires_at`. Every read filters expired events out, so
they answer `404` or simply do not appear — they are never served, even if the
`?include_expired=true` style query is added. They are also **not deleted on
read**: a background reaper deletes them hourly so that a read is never the
thing that mutates the retained set. `GET /stats` counts them separately
(`total`, `expired`) until the reaper claims them.

Storage is bounded too: the event store may hold at most `STORAGE_MAX / 5` bytes
of events, and once that is reached the **oldest live** events are evicted. An
evicted event is gone from every node that received it.

## Querying

`GET /events` filters the live set. All parameters are optional.

| Parameter | Behaviour |
| --- | --- |
| `collection` | Exact match. |
| `label` | Exact match against any one label. |
| `owner` | Exact match; case-insensitive on input. |
| `search` | Case-insensitive substring match against the canonical `data` text. |
| `blob` | Exact match; must be 64 hex characters. |
| `since` / `until` | Inclusive bounds on `created_at`. |
| `limit` | Page size, 1–100. Default 50. |
| `cursor` | Opaque keyset position, see below. |

Results are ordered `created_at` descending, with the event `id` as a
tiebreaker — the same ordering that makes pagination stable.

### Pagination

`next_cursor` is `"<created_at>:<id>"` and means **older than this position**.
Pass it back unchanged:

```bash
curl "http://localhost:3232/events?collection=notes&limit=50" | jq -r '.next_cursor'
curl "http://localhost:3232/events?collection=notes&limit=50&cursor=1761012000:6191ca75…"
```

`next_cursor` is empty on the last page, so a client never has to make a
trailing request just to discover the end. A page that exactly fills `limit` is
already the last page and returns an empty cursor.

The response envelope carries the list twice, `events` and `records`, with the
same contents; `records` exists for older clients. `cursor` echoes what you sent.

## Live streams

`GET /events/stream` is Server-Sent Events. Any browser reaches it with
`EventSource`; a terminal with `curl -N`. No WebSocket, no library.

```bash
curl -N "http://localhost:3232/events/stream?collection=notes"
```

Accepted live filters: `collection`, `label`, `owner`, `search`, `blob`,
`since`, `until`. `limit` is accepted and ignored. `cursor` is **rejected** with
`400`, because its "events older than here" semantics would suppress everything
new. Resume with `Last-Event-ID` instead.

### Frames

| Frame | When |
| --- | --- |
| `retry: 3000` | First frame. Tells `EventSource` to reconnect after 3 seconds. |
| `: connected` | First frame. A comment; carries no event. |
| `: keepalive` | Every 15 seconds, so the connection stays observable. |
| `event: event` + `id:` + `data:` | One delivered event, JSON in `data`, the event `id` in `id`. |

```text
retry: 3000
: connected

event: event
id: 6191ca75010530180a865daa5c1271dbf4172b487c55c5b966e73aa1d25475a9
data: {"id":"6191ca75…","owner":"ed25519:79b5…","collection":"notes","data":{…}}
```

### Resuming

Because every event frame carries `id`, a browser reconnects with
`Last-Event-ID: <last id>` and the server replays everything published after
that anchor, oldest first. A client that was briefly offline catches up instead
of silently losing events.

Replay is capped at the **newest 100** events, so a client returning after a long
gap lands on current state rather than a flood. If the anchor is unknown — the
event expired, was evicted, or the node never held it — the stream simply starts
live, no error. The subscriber is attached *before* the backlog is read, so
nothing published during that window can slip through the gap.

### Bounds

At most **256** concurrent subscribers per node; past that, `503`. A slow
consumer cannot pin a handler forever: each frame is written under a write
deadline, a failed write ends that stream, and a subscriber whose 64-slot queue
overflows loses the oldest pending frame rather than stalling the publisher.

## Federation

Nodes converge on the **union** of each other's event sets by pulling over the
same public HTTP API. There is no new protocol and no broker.

This is the property that makes Originless a replacement for a relay rather than
a different relay: relays do not replicate to each other, so a client holds N
independent sockets and is responsible for writing to every one of them. Here the
fan-out is the nodes' job. See [what this replaces](replacements.md).

```bash
docker run -d --name o1 -p 127.0.0.1:3233:3232 -e SYNC_NODES=http://localhost:3234 ghcr.io/besoeasy/originless:latest
docker run -d --name o2 -p 127.0.0.1:3234:3232 -e SYNC_NODES=http://localhost:3233 ghcr.io/besoeasy/originless:latest
```

Every 5 seconds each node, per peer:

1. reads its checkpoint from the `peer_cursors` table (survives restart),
2. `GET <peer>/events?limit=100&since=<checkpoint − 900s>`,
3. rebuilds each event without its server-assigned fields,
4. runs it through **the same validation as a publish** — signature, TTL,
   drift — so a peer cannot inject forged events,
5. dedupes on `id`, imports, and advances the checkpoint to the newest
   `created_at` it saw.

Details worth knowing:

- **Checkpoints are on `created_at`, with a 15-minute lookback.** That window
  matches the server's future-drift bound exactly, so an event a peer accepted
  with a slightly-ahead clock still lands on the next round. Dedupe makes the
  redundancy free.
- **Skipping is deliberate.** An event that fails validation is logged and
  skipped permanently; the checkpoint moves past it. A *persistence* failure is
  not: the round aborts so the checkpoint never advances past an event that was
  not stored.
- **Events only.** IPFS blobs stay on the node that accepted them. A federated
  event can reference a `blob` or a CID the receiving node cannot resolve.
- **Clock skew matters.** Backdating an event beyond the 15-minute window is not
  guaranteed to sync. Keep `created_at` at "now".

A round reads at most 50 pages; the next round continues from wherever it got
to, so a peer with a large backlog drains over several rounds instead of
stalling the loop.
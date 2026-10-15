# Architecture

For contributors: how the code is laid out, what guarantees the event store
makes, and which invariants are load-bearing.

- [Packages](#packages)
- [Startup](#startup)
- [The event store](#the-event-store)
- [Publish path](#publish-path)
- [Live stream path](#live-stream-path)
- [Federation path](#federation-path)
- [IPFS boundary](#ipfs-boundary)
- [Dashboard](#dashboard)
- [Tests](#tests)
- [Build and release](#build-and-release)
- [Load-bearing invariants](#load-bearing-invariants)

## Packages

```text
cmd/originless         main: PORT, config, IPFS client, router, signal handling
internal/config        environment parsing; derives the event byte budget
internal/events        event schema, verification, store, reaper, filters, SSE, HTTP handler
internal/sync          peer pull loop over the public /events endpoints
internal/ipfs          minimal Kubo HTTP API client
internal/server        router, CORS, embedded dashboard, stats/upload/cid/health handlers
internal/testutil      test helpers: signs events with the same recipe as a client
scripts/example-event.go   reproducible signed event used by the docs
docs/                  this documentation
```

The dependency direction is one-way: `server` and `sync` both call into
`events`; nothing calls back out. `events` knows nothing about HTTP routing
beyond its own handler, which is why the same store can be driven by a client,
a test, or a federation round.

## Startup

`main` does five things, in order: validate `PORT`, build the `config` from the
environment, construct the IPFS client for `127.0.0.1:5001`, build the router,
and serve until a signal arrives. On `SIGINT`/`SIGTERM` it cancels the root
context (stopping the reaper and the sync loop), shuts the HTTP server down
within 5 seconds, and closes the event database. `entry.sh` forwards the same
signal to both the Go app and the Kubo daemon it supervises.

`server.NewRouterWithOptions` opens the store, starts the hourly reaper, starts
the federation loop when `SYNC_NODES` is set, and arranges for the store to
close when the context is done. Everything long-lived hangs off that one
context.

`ReadTimeout` and `WriteTimeout` are deliberately zero: a stream response must
not be cut by a server-level deadline. Liveness is enforced per write instead,
inside the SSE handler.

## The event store

`events.Store` is two things wearing one coat:

- an in-memory `map[string]*Event` that answers every query, and
- a SQLite table (`events`) that makes the set durable.

Reads, filtering, sorting, pagination, statistics and eviction all happen in Go
over the map. SQLite exists to survive restarts, not to query. That is why
label and collection filtering need no index, and why memory — not disk — is the
real ceiling on a node.

Every insert, eviction and reap writes through to SQLite, so the two never
disagree. The connection is capped at a single connection with WAL journaling,
which removes write contention entirely at this scale and keeps reads and the
periodic write consistent.

At startup the store:

1. creates the `events` and `peer_cursors` tables if absent,
2. deletes every row whose `expires_at` has passed,
3. loads the survivors into the map,
4. drops the oldest events if the store is over its byte budget.

`Event.Size` is the byte length of the request body as the client sent it. It is
what the budget measures, so a client that pads an event with whitespace makes
it occupy more of the node's budget — a deliberate property, since the limit is
about bytes on disk.

### Concurrency

A single `sync.Mutex` guards the map, the subscriber set and the byte counters.
The lock is held only for bookkeeping: the fan-out to subscribers happens after
it is released, and each event is deep-copied (`cloneEvent`) on the way out so
callers can never mutate stored state or observe a torn read.

Subscriber channels are buffered (64) and written **non-blockingly**: a consumer
that cannot keep up loses the pending frame and is logged, instead of stalling
the publisher. Slow readers degrade themselves rather than the node.

### Expiry

Expiry is enforced at read time and never by a read. Expired events stay
resident — invisible to every query, still counted in `total` — until the
reaper's hourly pass deletes them. The invariant: *a read never mutates the
retained set*, so a reap is the only thing that can shrink it and the observable
behaviour of the API does not depend on when the reaper happens to run.

## Publish path

```text
POST /events
  → MaxBytesReader(8 KiB + 1)
  → content-type check
  → validateEvent(raw, now)
      · reject `id`, reject unknown fields, reject trailing JSON
      · require owner, collection, created_at, expires_at, labels, sig, data
      · normalize owner to lower case
      · collection regex, label regex, duplicate-label check
      · created_at ≤ now + 15 min, expires_at > created_at, TTL ≤ 1 year,
        expires_at > now
      · canonicalize data, require a JSON object
      · message = owner:collection:created:expires:data:blob:labels
      · digest = sha256(message); verify ed25519(sig, digest, owner)
  → id = hex(digest)
  → insert: evict if over budget, INSERT OR IGNORE, add to map, fan out
  → 201 {id, stored_at}   |   200 {duplicate: true} if the id was known
```

`validateEvent` is a pure function of `(raw, now)`. That is what lets the
publish path, the federation importer and the tests share one definition of
"valid".

## Live stream path

```text
GET /events/stream
  → parse the filter, reject ?cursor
  → subscribe (cap 256)
  → set SSE headers
  → write "retry: 3000" + ": connected"
  → if Last-Event-ID: replay everything published after that anchor,
    oldest first, capped at the newest 100
  → loop: 15s keepalive comment | event frame from the subscriber channel
```

The subscription is attached *before* the backlog is read, and replayed IDs are
remembered for the life of the connection, so the handover between "catching up"
and "live" cannot drop or duplicate an event.

Every frame is written under a write deadline of two keepalive intervals. A
wedged client — stuck TCP, full kernel buffers — fails its write and the handler
returns, instead of pinning a goroutine and a slot in the subscriber cap
forever.

An unknown `Last-Event-ID` is not an error: the anchor is not in the store, so
nothing is replayed and the stream continues live.

## Federation path

```text
every 5s, per peer
  → read the checkpoint from peer_cursors
  → GET <peer>/events?limit=100&since=<checkpoint − 900s>[&cursor=…]
  → for each event: rebuild the publishable form (drop id, stored_at, size)
  → store.Accept(raw, now) → validateEvent + insert
      · ErrInvalidEvent → log once, skip, let the checkpoint move past it
      · anything else   → abort the round, leave the checkpoint where it was
  → write the newest created_at back to peer_cursors
```

Two rules carry the correctness:

- **Never trust, always verify.** Imported events go through the same
  `validateEvent` as a publish, so a peer cannot inject forged events, or events
  that claim to be ancient or long-lived.
- **Never advance the cursor past unstored work.** Skipping is for events that
  are *permanently* invalid. A transient persistence failure aborts the round,
  because moving the checkpoint forward would silently drop that event forever.

The 900-second lookback equals the server's future-drift bound: a peer may
accept an event whose `created_at` is up to 15 minutes ahead, and without the
lookback such an event would fall outside the next round's window and never be
fetched. Dedupe on `id` makes the repeated fetches free.

## IPFS boundary

`internal/ipfs` is a thin client over Kubo's HTTP API — no library, no CGO, no
opinion about the repo. Its job is to be safe under load:

- Responses are read through `io.LimitReader`, so a misbehaving node cannot
  exhaust memory.
- `Add` streams a multipart body through an `io.Pipe` from spooled temp files
  rather than buffering an upload in memory.
- Every endpoint gets explicit dial, TLS-handshake and response-header
  timeouts.
- `StatusError` carries Kubo's HTTP status, so handlers can distinguish "this
  node does not have it" from "the node is broken".

The spool-then-add design in `internal/server/upload.go` is what makes the 1 GiB
upload limit safe: parts land on disk first, free space is checked before any
body is read, paths are cleaned against traversal, and temp files are removed on
every exit path including failures.

## Dashboard

`internal/server/static/index.html` is one embedded file — no build step, no
bundler, no external assets, served with `Cache-Control: no-cache`. It is the
product demo: node status, a signed chat, a shared 256×256 canvas and an upload
dropzone, all driven by the public API only.

Two details are load-bearing:

- **It carries its own cryptography.** `crypto.subtle` is unavailable on
  plain-HTTP origins, so `FallbackCrypto` implements SHA-256 and Ed25519 in
  plain JS and is used whenever `subtle` is missing, with Web Crypto preferred
  when present. A stored Web Crypto JWK is migrated in, so an existing browser
  identity survives the switch.
- **History is merged, not raced.** Canvas subscribes *before* loading history
  and buffers live events until the history is applied, then replays both in
  `(created_at, id)` order — otherwise a buffered event older than a history
  event for the same cell would regress that cell.

## Tests

`go test ./...` is the whole gate. There are no external services: Kubo is
faked with `httptest` servers, SQLite runs against `t.TempDir()`, and
`internal/testutil` signs events with the same recipe a client would use — so a
change to the signing scheme breaks tests rather than silently producing a
different protocol.

The tests that encode deliberate behaviour, rather than just exercising lines:

| Test | Pins down |
| --- | --- |
| `TestExpiredEventsAreNeverServed` | Expired events are invisible to every read, and reads do not delete them |
| `TestEventStreamResumesFromLastEventID` | Reconnect replays the gap without re-sending the anchor |
| `TestEventStreamIgnoresUnknownLastEventID` | A stale anchor degrades to a live stream |
| `TestEventQueryOmitsCursorOnLastPage` | A full page is the last page; no trailing request |
| `TestEventOwnerIsCaseNormalized` | One key yields one id and matches any-case filters |
| `TestPersistentStoreSurvivesReopen` | Durability across a simulated restart |
| `TestSyncOnceRejectsForgedEvents` | Federation re-verifies, it does not trust |
| `TestUploadRejectsPathTraversal` | Multipart paths are contained |
| `TestHealthzReportsUnavailableNode` | Health never lies about a dead node |

## Build and release

The `Dockerfile` is two stages: a `golang:alpine` builder producing a static
`CGO_ENABLED=0` binary, and an `alpine` runtime with the `kubo` package,
`ca-certificates` and `curl`. SQLite is `modernc.org/sqlite`, a pure-Go
translation, which is what keeps CGO off and the binary self-contained.

`entry.sh` initialises the Kubo repo on first start, applies the fixed storage
GC policy, starts `ipfs daemon --enable-gc`, waits for the RPC to answer, and
only then execs the app — so the app never starts against a node that is not
ready. Both children receive the container's stop signal.

Releases are manual: a `workflow_dispatch` on `.github/workflows/release.yml`
takes a `1.0.5`-style tag, runs vet/test/build, builds multi-arch
`linux/amd64`+`linux/arm64` images, pushes them to GHCR, signs them with
cosign, and creates the GitHub release. CI itself is just vet, build, test on
the Go version `go.mod` declares.

## Load-bearing invariants

If you change one of these, the corresponding test should change with it:

1. **`validateEvent` is the single definition of a valid event.** Publish and
   federation import must never diverge.
2. **The signature covers `sha256(message)`, and `id` is that same digest.**
   Both are client-visible protocol, not implementation detail.
3. **Canonicalization is part of the signature.** Key sorting and number
   preservation are what make `data` hashable.
4. **Reads never mutate the retained set.** Only the reaper and eviction do.
5. **Expired means invisible**, not deleted — and indistinguishable from absent.
6. **A sync cursor never passes an event that was not stored.**
7. **The IPFS node is never exposed.** Only port 3232 leaves the container.
8. **Settings are resource bounds, never access control.** If a change makes a
   setting sound like a security control, the documentation needs to change too.
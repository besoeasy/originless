# Configuration and operations

- [Environment variables](#environment-variables)
- [What is stored where](#what-is-stored-where)
- [Sizing storage](#sizing-storage)
- [Access control](#access-control)
- [Federation](#federation)
- [Observability](#observability)
- [Behind a reverse proxy](#behind-a-reverse-proxy)
- [Backup and restore](#backup-and-restore)
- [Troubleshooting](#troubleshooting)

## Environment variables

Every setting has a working default, so the container runs with no
configuration at all.

| Variable | Default | Meaning |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Soft limit for the IPFS repository. Accepts `KB`/`MB`/`GB`/`TB` (with or without `iB`), or a plain byte count. Also sets the event budget, below. Rejected below 1 MB. |
| `PORT` | `3232` | HTTP port inside the container. Must be 1–65535. |
| `SYNC_NODES` | *none* | Comma-separated base URLs of peer nodes to federate with. Each must start with `http://` or `https://`. |

```bash
docker run -d --name originless \
  -p 3232:3232 \
  -e STORAGE_MAX=200GB \
  -e SYNC_NODES=https://node-a.example,https://node-b.example \
  -v originless-data:/data \
  ghcr.io/besoeasy/originless:latest
```

Two settings are **fixed by design** and cannot be overridden, because they
interact with how uploads are stored: the IPFS garbage collector runs hourly at a
**90%** watermark, and the node boots with the `lowpower` profile and
`Routing.Type=dhtclient`. The startup line reports the effective values:

```text
originless: settings storage_max="20GB" event_bytes_budget=4294967296 events_db="/data/events.db" sync_nodes=0
originless: storage max=20GB, GC watermark=90%, period=1h
```

## What is stored where

```text
/data
├── events.db        SQLite: signed events + per-peer sync checkpoints (plus -wal/-shm)
└── ipfs/            Kubo repository: blocks, config, keys
```

Mount a volume at `/data` to keep both across container replacement. Without
one, `docker rm` deletes the event log *and* every uploaded CID, and those CIDs
cannot be recovered from the public IPFS network because uploads are never
pinned.

The container runs as an unprivileged `originless` user and only exposes
`3232/tcp`. Kubo's HTTP API and the libp2p swarm stay on loopback inside the
container.

## Sizing storage

`STORAGE_MAX` governs three things at once:

1. **IPFS repository** — Kubo garbage collects at 90% of `STORAGE_MAX`.
2. **Event payload budget** — set to `STORAGE_MAX / 5`. Once live events exceed
   it, the oldest are evicted and counted in `events_evicted` at `/stats`.
3. **Upload admission** — an upload is rejected with `507` when the spool
   filesystem cannot plausibly hold 1 GiB plus margin.

Uploads are **not pinned**. A CID can become unavailable after garbage
collection, by design: the node is a temporary landing spot for
content-addressed data, not an archive. Treat a CID as a reference and keep your
own copy if you need the bytes.

If you want uploads to survive GC, run a dedicated Kubo node and use
`ipfs pin add`; Originless always adds with `pin=false`.

## Access control

**There is none, deliberately.** No accounts, no passwords, no API tokens, and
CORS is `*` because there are no credentials for a browser to withhold.

An Ed25519 signature proves **authorship** — who wrote an event — not
**permission** to write it. Anyone who can reach the port can publish to any
collection, read every event, and upload content. The network boundary is the
trust boundary.

To keep a run private, bind to loopback and reach it over SSH:

```bash
docker run -d --name originless -p 127.0.0.1:3232:3232 -v originless-data:/data ghcr.io/besoeasy/originless:latest
ssh -L 3232:127.0.0.1:3232 you@your-server
```

For multi-user applications, enforce authorization in your own client or in a
reverse proxy in front of Originless. The settings above are resource bounds,
not access control.

One deliberate trade-off worth naming: **peers are not authenticated**. A node
listed in `SYNC_NODES` gets its events re-verified and re-stored, but it can also
flood a node with valid-but-useless events until the byte budget evicts real
ones. Only point `SYNC_NODES` at URLs you trust.

## Federation

Set `SYNC_NODES` on each node to the other's base URL and they converge on the
union of both event logs over plain HTTP, every 5 seconds.

```bash
docker run -d --name o1 -p 127.0.0.1:3233:3232 -e SYNC_NODES=http://localhost:3234 ghcr.io/besoeasy/originless:latest
docker run -d --name o2 -p 127.0.0.1:3234:3232 -e SYNC_NODES=http://localhost:3233 ghcr.io/besoeasy/originless:latest
```

Publish on one node, query the other:

```bash
curl "http://localhost:3233/events?collection=notes"
```

Operational notes:

- **Events only.** IPFS blobs never move; a synced event may reference content
  the other node cannot resolve.
- **Checkpoints persist** in SQLite, so a restart resumes where it left off.
- **Backdated events** beyond the 15-minute drift window may never sync. Keep
  `created_at` at "now" on every client.
- **First sync is incremental.** A peer with a large backlog drains over
  several rounds (50 pages each); convergence is fast but not instant.
- **Removed peers** are harmless — the stored checkpoint for an address you no
  longer sync with is simply never read again.

The protocol is described in full under [federation](events.md#federation).

## Observability

| What | Where |
| --- | --- |
| Liveness of app + IPFS node | `GET /healthz` — `200` healthy, `503` degraded |
| Container healthcheck | `wget -qO- http://127.0.0.1:3232/healthz` every 30s |
| Repository and event metrics | `GET /stats` |
| Runtime events | `docker logs -f originless` |

The dashboard's **Node status** panel is a view onto `/stats` and refreshes every
30 seconds: repository size against `STORAGE_MAX`, live/total/expired events,
distinct signers, stream subscribers against the 256 cap, top collections and
labels, and the on-disk size of the event database.

`events.count` counts live events; `events.total` also includes expired ones the
hourly reaper has not reclaimed. If `events_evicted` climbs, the event budget is
full — raise `STORAGE_MAX` or shorten TTLs.

## Behind a reverse proxy

Set a long or disabled read timeout for `/events/stream`: the connection is
meant to stay open, and idle periods are filled by a 15-second keepalive
comment. Originless already sends `Cache-Control: no-cache, no-transform` and
`X-Accel-Buffering: no`, but a proxy that buffers anyway will stall delivery.

Serve over **HTTPS** in front of a proxy. Browsers only expose
`crypto.subtle` in secure contexts; the bundled dashboard carries a pure-JS
Ed25519 signer as a fallback so the demos still work over plain HTTP, but your
own clients should not have to.

## Backup and restore

Everything durable is under `/data`, so a backup is a copy of that directory
with the container stopped:

```bash
docker stop originless
docker run --rm -v originless-data:/data -v "$PWD":/backup \
  alpine tar czf /backup/originless-$(date +%F).tar.gz -C /data .
docker start originless
```

Restoring is the same operation in reverse: stop the container, replace the
contents of `/data`, start it. Event IDs are content-derived, so a restored node
accepts and re-deduplicates anything republished from another node. Copying a
live `events.db` without stopping can capture a torn WAL; copy the `-wal` and
`-shm` files too, or better, stop first.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `/healthz` returns `503`, `{"status":"degraded"}` | Kubo is unreachable — it may still be starting, or the repo is corrupt. `docker logs originless`; confirm the daemon is running before the app is. |
| `/stats` returns `503` | Same as above: stats are read from the node. |
| Events vanish over time | Expected. Either their `expires_at` passed (`events.expired`), or the byte budget evicted the oldest (`events_evicted` climbs). |
| A CID returns `available: false` | Normal for unpinned content after GC, or for a CID that only exists on the public network. Lookups are offline, so the answer is immediate. |
| `/cid/{cid}` returns `503` | The IPFS node is unreachable. Nothing is reported about availability, because the question could not be asked. |
| `400 invalid collection` | Collections must match `^[a-z0-9/_-]{1,32}$`. Lower case, no dots. |
| `401` on publish | The signature does not verify: the message was built in the wrong field order, `owner` was not lower-cased before signing, the data was not canonicalized, or the signature covered the message instead of its SHA-256 digest. |
| `503 too many event stream subscribers` | The 256-subscriber cap is full. Check for clients that reconnect in a loop without honouring `Last-Event-ID`. |
| `507` on upload | Not enough free space on the spool filesystem for a 1 GiB upload. `/up` reserves headroom before reading the body. |
| `413` on upload | Over the 1 GiB limit. Split the upload or use `/upf` per directory. |
| `502 IPFS add failed` | The node rejected the add. `docker logs` for the Kubo side. |
| Peers do not converge | Check `/healthz` on the peer, then clock skew: `created_at` is the sync key, and events backdated more than 15 minutes may be skipped. |
| `400 cursor is not supported on the stream` | Use `Last-Event-ID` to resume a stream; `cursor` is list pagination only. |

## Running from source

Requires Go 1.24+ and, for a full single-process experience, a `kubo` install on
`localhost:5001`.

```bash
go vet ./...      # lint
go build ./...    # build
go test ./...     # test
./podman.sh       # build the image and run it locally, foreground, --rm
```

The app reads `PORT`, `STORAGE_MAX` and `SYNC_NODES`; when running outside the
container it also expects `/data` to be writable, or point
`config.EventsDBPath` elsewhere in code. See [architecture](architecture.md).
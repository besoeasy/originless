# Originless

<p align="center">
  <strong>A single-container backend for real-time web apps.</strong><br/>
  Signed, persistent events with live updates, content-addressed file uploads, and optional multi-node federation — no database server, message broker, or account system to run.
</p>

<p align="center">
  <a href="https://github.com/besoeasy/originless/actions/workflows/ci.yml"><img src="https://github.com/besoeasy/originless/actions/workflows/ci.yml/badge.svg" alt="CI"/></a>
  <a href="https://github.com/besoeasy/originless/pkgs/container/originless"><img src="https://img.shields.io/badge/ghcr.io-originless-blue?logo=docker" alt="Container image"/></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/besoeasy/originless" alt="Go version"/></a>
  <a href="https://github.com/besoeasy/originless/releases"><img src="https://img.shields.io/github/v/release/besoeasy/originless" alt="Latest release"/></a>
</p>

Originless is a small Go service with an embedded [Kubo](https://github.com/ipfs/kubo)
IPFS node, shipped as one image. It gives a browser app two things it normally
has to assemble from several cloud services: **signed events** that persist and
stream live, and **content-addressed uploads**. Set `SYNC_NODES` and separate
nodes converge on the same event log over plain HTTP.

## Quick start

```bash
docker run -d --name originless -p 3232:3232 -v originless-data:/data \
  ghcr.io/besoeasy/originless:latest
```

Open the dashboard at <http://localhost:3232> — node status plus a live chat, a
shared canvas, and an upload dropzone. From this checkout, `./podman.sh` builds
the image and runs it in the foreground.

Publish an event, watch it arrive, upload a file:

```bash
curl -X POST http://localhost:3232/events -H 'Content-Type: application/json' \
  -d '{"owner":"ed25519:…","collection":"notes","created_at":1758420000,
       "expires_at":1761012000,"data":{"note":"hello"},"labels":[],
       "sig":"…"}'

curl -N "http://localhost:3232/events/stream?collection=notes"
curl -F "file=@hello.txt" http://localhost:3232/up
```

The `…` in that body is the part you have to do yourself: events are signed by
the client with an Ed25519 key, so there is no server account to log into. The
complete recipe — with a runnable publisher and a reproducible worked example —
is in the [quickstart](docs/quickstart.md).

## What you get

| | |
| --- | --- |
| **Signed events** | Immutable, Ed25519-verified JSON documents with TTL expiry, stored in SQLite, never served after expiry. An Ed25519 signature is the only credential in the protocol. |
| **Live updates** | Server-Sent Events, no WebSocket server. Reconnecting clients resume from their last event ID. |
| **IPFS uploads** | Files and folders addressed by CID through the embedded Kubo node. Unpinned, so treat a CID as a reference. |
| **Federation** | `SYNC_NODES` makes nodes converge on the union of their event sets, re-verifying every signature on the way in. |
| **CORS everywhere** | Any origin may call the API — there are no credentials for a browser to withhold. |

## Why the files ride on IPFS

The event store and the live stream are what make Originless compact. The IPFS
layer is what makes its content *self-verifying and host-independent*: uploads
are not files parked behind this service's URL, they are content-addressed
blocks whose name (the CID) is the hash of the bytes themselves.

- **The name proves the bytes.** Whatever serves a block — this node, a public
  gateway, a peer who pinned it — the client can verify it against the CID
  before trusting it. There is no "believe the host" step, and a tampered copy
  cannot still answer to the same name.
- **Content is not hostage to one host.** Any IPFS gateway can serve a CID
  whose blocks live somewhere reachable, and this node is only one possible
  home among them. Taking content down becomes a distributed chase across
  holders and mirrors, not a request to one server.
- **Readers can stay private.** A fetch needs the CID, not the origin: pull
  from any gateway — or straight off the swarm — without ever speaking to the
  node that first held the upload. The origin's access log cannot profile its
  readers if it is never in the read path.

The trade, stated plainly: uploads are not pinned here, so this node's own copy
is reclaimable by garbage collection, and "still out there" is
availability-by-others, not a promise. Pin what must survive — see
[configuration → sizing storage](docs/configuration.md#sizing-storage).

## What this replaces

Originless stands in for three services that are normally deployed separately:

| Role | Normally | Here |
| --- | --- | --- |
| Events and live subscriptions | a Nostr relay over WebSocket | `POST /events` plus SSE at `/events/stream` |
| File upload and fetch | a Blossom-style blob server | `POST /up` plus `/cid/{cid}`, CIDs any gateway serves |
| Discovery, connection auth, upkeep | relay lists, NIP-42 handshakes, N relays to keep alive | one URL, no handshake, one volume |

Relays do not replicate to each other, so a client holds N sockets and writes to
every one of them; Originless nodes pull from each other over plain HTTP,
re-verify every signature and converge on the union of their events — so a node
dying is a rebuild, not data loss. The full mapping, the reasoning, and an
honest list of where the analogy does not hold are in
[what this replaces](docs/replacements.md).

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Soft limit for the Kubo repository. Stored events are capped at a fifth of this value; beyond that the oldest live events are evicted. |
| `PORT` | `3232` | HTTP port inside the container. |
| `SYNC_NODES` | *none* | Comma-separated base URLs of peer nodes to federate with. |

Mount a volume at `/data` to keep the IPFS repository and event database across
container replacement. Garbage collection runs hourly at a fixed 90% watermark.

## Documentation

| | |
| --- | --- |
| [Quickstart](docs/quickstart.md) | Run a node, publish a signed event, watch it stream |
| [What this replaces](docs/replacements.md) | Relay and blob-server mapping, why it is more resilient, where the analogy breaks |
| [HTTP API reference](docs/api.md) | Every route, parameter, body and status code |
| [Event protocol](docs/events.md) | Signing recipe, canonical JSON, IDs, filters, pagination, SSE, federation |
| [Configuration and operations](docs/configuration.md) | Ports, storage math, access control, backup, troubleshooting |
| [Architecture](docs/architecture.md) | Package layout, storage model, concurrency, tests, release process |
| [All docs](docs/index.md) | Documentation index |

## Two things to know

**There is no access control, by design.** No accounts, no passwords, no API
tokens: an Ed25519 signature proves who wrote an event, not that they were
allowed to. CORS is `*` because there is nothing to protect. Bind to
`127.0.0.1` to keep a run private, and put authorization in your own client or a
reverse proxy when you need it. Full reasoning in
[configuration → access control](docs/configuration.md#access-control).

**Uploads are ephemeral.** Nothing is pinned, so Kubo's garbage collector may
reclaim a CID. Keep your own copy of anything you need to survive that.

## Development

Requires Go 1.24+, and a `kubo` install for the full single-process experience.

```bash
go vet ./...     # lint
go build ./...   # build
go test ./...    # test
```

## Links

- [Kubo](https://github.com/ipfs/kubo)
- [IPFS HTTP Gateway specification](https://specs.ipfs.tech/http-gateways/)
- [inbrowser.link](https://inbrowser.link/ipfs/) to open a CID in a browser
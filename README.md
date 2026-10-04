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

Originless is a small Go HTTP service with an embedded [Kubo](https://github.com/ipfs/kubo) IPFS node, packaged in one image. You get a self-hosted backend you can start with a single command:

- **Signed events** — immutable, Ed25519-verified JSON documents with TTL expiry, persisted in SQLite and never served after expiry
- **Live updates** — Server-Sent Events, no WebSocket server required; reconnecting clients resume from their last event ID
- **IPFS uploads** — files and folders addressed by CID, no pinning by default
- **Federation** — set `SYNC_NODES` and nodes converge on the same event set over plain HTTP
- **Built-in demos** — a shared canvas, chat, event stream, and upload dashboard
- **CORS enabled** — browser apps can call the API from any origin

## Quick start

Docker is the supported runtime:

```bash
docker run -d --name originless -p 3232:3232 -e STORAGE_MAX=20GB ghcr.io/besoeasy/originless:latest
```

Open the dashboard at [http://localhost:3232](http://localhost:3232).

```bash
docker logs -f originless   # follow logs
docker stop originless      # stop (data preserved in the container layer)
docker rm originless        # remove (deletes all data)
```

To keep the IPFS repository and event database across container replacement, mount a volume at `/data`:

```bash
docker run -d --name originless -p 3232:3232 -v originless-data:/data ghcr.io/besoeasy/originless:latest
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Human-readable soft limit for the Kubo repository. Stored events are capped at a fifth of this value; beyond that the oldest live events are evicted. |
| `PORT` | `3232` | HTTP port inside the container. |
| `SYNC_NODES` | *none* | Comma-separated base URLs of peer Originless nodes to federate with. |

Automatic garbage collection runs hourly at a fixed **90%** watermark. Because uploads are unpinned, a CID may become unavailable after garbage collection.

## Federation

Set `SYNC_NODES` on each node to the other's base URL and the two exchange events over plain HTTP every 5 seconds:

```bash
docker run -d --name o1 -p 127.0.0.1:3233:3232 -e SYNC_NODES=http://localhost:3234 ghcr.io/besoeasy/originless:latest
docker run -d --name o2 -p 127.0.0.1:3234:3232 -e SYNC_NODES=http://localhost:3233 ghcr.io/besoeasy/originless:latest
```

Each node pulls `GET /events?since=...` from its peers, re-verifies every signature, and dedupes by event ID, so they converge on the union of both event sets. Only **events** are synced — IPFS blobs stay on the node that accepted them, and each node keeps its own `/data/events.db`.

> [!NOTE]
> Sync checkpoints on `created_at`, so client clocks should be roughly in step and `created_at` should be "now" — events backdated beyond the 15-minute drift window are not guaranteed to sync. Checkpoints are persisted, so a node resumes where it left off after a restart.

Like every other part of Originless there is no authentication — expose the API only on networks you trust.

## Access control

There is none, by design. No user accounts, no passwords, no API tokens: an Ed25519 signature on an event is the only credential in the protocol, and it proves **authorship** — who wrote an event — not **permission** to write it. Any client that can reach the port can publish to any collection, and CORS is `*` because there are no credentials for a browser to withhold.

That makes the network boundary the trust boundary. To keep a run private, bind to localhost:

```bash
docker run -d --name originless -p 127.0.0.1:3232:3232 ghcr.io/besoeasy/originless:latest
```

For multi-user applications, enforce authorization in your own client or in a reverse proxy in front of Originless. The settings above are resource bounds, not access control.

## Upload and download files

```bash
# single file
curl -F "file=@hello.txt" http://localhost:3232/up

# folder via /upf
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

Both return the resulting CID. For content available through the public IPFS network, use [inbrowser.link](https://inbrowser.link/ipfs/), [Helia Verified Fetch](https://github.com/ipfs/helia-verified-fetch), or a gateway such as [Rainbow](https://github.com/ipfs/rainbow).

## API

The complete request and response reference is in [`docs/api.md`](docs/api.md).

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/` | Dashboard and live demos |
| `GET` | `/healthz` | Container health |
| `GET` | `/stats` | IPFS and event metrics |
| `POST` | `/up` | Upload a file or folder |
| `POST` | `/upf` | Explicit folder upload |
| `GET` | `/cid/{cid}` | JSON metadata and availability for a CID |
| `POST` | `/events` | Publish a signed event |
| `GET` | `/events` | Query events with filters and pagination |
| `GET` | `/events/{id}` | Retrieve one event |
| `GET` | `/events/stream` | Stream events over SSE |

Events are stored in SQLite at `/data/events.db` and survive restarts. Uploaded content is ephemeral by default — mount a volume at `/data` to keep it, and remember unpinned CIDs can be garbage-collected.

### Signed events

An event is a small JSON document signed by its creator. The signature is verified with the Ed25519 public key in `owner`; the server manages no accounts or passwords.

```json
{
  "owner": "ed25519:<64-hex-public-key>",
  "collection": "originless/chat",
  "created_at": 1758420000,
  "expires_at": 1789956000,
  "data": {"message": "Hello from the browser"},
  "labels": ["chat:lobby"],
  "sig": "<128-hex-signature>"
}
```

Use `/events/stream` to receive matching events live. Event documents are limited to 8 KiB and one year of TTL.

## Development

Requires Go 1.24+ and, for the full single-binary experience, a `kubo` install (or Docker).

```bash
go vet ./...     # lint
go build ./...   # build
go test ./...    # test
```

Build the image locally:

```bash
docker build -t originless:dev .
```

## Links

- [API reference](docs/api.md)
- [Kubo](https://github.com/ipfs/kubo)
- [IPFS HTTP Gateway specification](https://specs.ipfs.tech/http-gateways/)

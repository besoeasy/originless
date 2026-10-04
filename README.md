# Originless

Originless is a single-container backend for real-time web applications: a
small Go HTTP service plus an embedded [Kubo](https://github.com/ipfs/kubo)
IPFS node, in one image. You get signed, persistent events with live
server-sent updates, content-addressed file uploads, and optional
multi-node federation — with no database server, message broker, or account
system to run.

## What you get

- **Signed events** — immutable, Ed25519-verified JSON documents with TTL
  expiry, persisted in SQLite at `/data/events.db`. Events survive process
  restarts and are never served after expiry.
- **Live updates** — Server-Sent Events at `/events/stream`; no WebSocket
  server required. Reconnecting clients resume from their last event ID.
- **IPFS uploads** — files and folders are added with `pin=false` and
  addressed by CID.
- **Federation** — set `SYNC_NODES` and nodes converge on the same event set
  over plain HTTP. Only events are synced; IPFS blobs stay per-node.
- **Built-in demos** — a shared 256×256 canvas, chat, event stream, and
  upload dashboard, served at `/`.
- **CORS enabled** — browser applications can call the HTTP API from another
  origin.

## Run with Docker

Docker is the supported runtime. Start the published image with one command:

```bash
docker run -d --name originless -p 3232:3232 -e STORAGE_MAX=20GB ghcr.io/besoeasy/originless:latest
```

Open the dashboard at [http://localhost:3232](http://localhost:3232).

The container has no volume mount. Its IPFS repository and events database live in the container filesystem, so stopping the container preserves its writable layer, while removing the container deletes that data.

```bash
docker logs -f originless
docker stop originless
docker rm originless
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Human-readable soft limit for the Kubo repository. |
| `PORT` | `3232` | HTTP port inside the container. |
| `MAX_EVENTS` | `10000` | Event cap across the SQLite store. The oldest live events are evicted once it is reached. |
| `SYNC_NODES` | *none* | Comma-separated base URLs of peer Originless nodes to federate with. |

## Syncing between two Originless nodes

Set `SYNC_NODES` on each node to the other's base URL and the two exchange
events over plain HTTP every 5 seconds:

```bash
podman run -d --name o1 -p 127.0.0.1:3233:3232 -e SYNC_NODES=http://localhost:3234 ghcr.io/besoeasy/originless:latest
podman run -d --name o2 -p 127.0.0.1:3234:3232 -e SYNC_NODES=http://localhost:3233 ghcr.io/besoeasy/originless:latest
```

Each node pulls `GET /events?since=...` from its peers, re-verifies every
signature, and dedupes by event ID, so the two converge on the union of both
event sets. Like every other part of Originless there is no authentication —
expose the API only on networks you trust.

Only events are synced. IPFS uploads stay on the node that accepted them,
and each node has its own `/data/events.db` — sync keeps the *contents*
converged, not the files on disk.

To keep the IPFS repository and the events database across container
replacement, mount a volume at `/data`:

```bash
podman run -d --name originless -p 127.0.0.1:3232:3232 \
  -v originless-data:/data \
  ghcr.io/besoeasy/originless:latest
```

Automatic garbage collection is enabled at a fixed **90%** watermark and runs hourly. `STORAGE_MAX` is the only storage setting exposed as a Docker environment variable. Because uploads are unpinned, a CID may become unavailable after garbage collection.

### Access control

There is none, by design. Originless has no user accounts, no passwords and no API token: an Ed25519 signature on an event is the only credential in the protocol, and it proves **authorship** — who wrote an event — not **permission** to write it. Any client that can reach the port can publish to any collection, and CORS is `*` because there are no credentials for a browser to withhold.

That makes the network boundary the trust boundary. The container publishes port 3232 on every interface so devices on your LAN can open the shared canvas and chat, which is the intended use. To keep a run private:

```bash
podman run -d --name originless -p 127.0.0.1:3232:3232 ghcr.io/besoeasy/originless:latest
```

For multi-user applications, enforce authorization in your own client or in a reverse proxy in front of Originless. The settings above are resource bounds, not access control.

## Upload and download files

Upload a single file:

```bash
curl -F "file=@hello.txt" http://localhost:3232/up
```

Upload a folder using `/upf`:

```bash
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

The response contains the resulting CID.

For content available through the public IPFS network, use [inbrowser.link](https://inbrowser.link/ipfs/), [Helia Verified Fetch](https://github.com/ipfs/helia-verified-fetch), or a dedicated gateway such as [Rainbow](https://github.com/ipfs/rainbow).

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

Events are stored in SQLite at `/data/events.db` and survive process restarts. Content uploaded to IPFS is also ephemeral by default: the container does not mount a persistent volume and does not pin uploads.

## Signed events

An event is a small JSON document signed by its creator. The signature is verified by the server using the Ed25519 public key in `owner`; the server does not manage user accounts or passwords.

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

## Links

- [GitHub repository](https://github.com/besoeasy/originless)
- [API reference](docs/api.md)
- [Kubo](https://github.com/ipfs/kubo)
- [IPFS HTTP Gateway specification](https://specs.ipfs.tech/http-gateways/)

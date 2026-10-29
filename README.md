# Originless

**Upload a file at home. Fetch it from anywhere in the world.**

Originless is a single-container backend for people who want to host files without internet configs. No domain to buy, no DNS records to manage, no server to expose. A file's address is its own hash — a CID — and that CID can be fetched from any node in the world. IPFS does the networking under the hood; you run one container.

## How it works

1. Run Originless at home with one command.
2. Upload a file — it's stored on an embedded [IPFS](https://ipfs.tech) node and you get back its CID (the hash of the content).
3. Anyone, anywhere, fetches the file by its CID — from your node or any node that has a copy. The address never changes because the content never changes.

No domain, no origin server, no records to point anywhere. The file is the address — that's why it's *originless*.

## Quick start

```bash
docker run -d --name originless -p 3232:3232 -e STORAGE_MAX=20GB ghcr.io/besoeasy/originless:latest
```

Open the built-in dashboard at [http://localhost:3232](http://localhost:3232).

## Upload and fetch

```bash
# upload — returns the CID
curl -F "file=@hello.txt" http://localhost:3232/up

# fetch it back through this node's gateway
curl http://localhost:3232/ipfs/<cid>
```

That CID now works from anywhere, for anyone with an IPFS-capable client or gateway:

- Browser: [inbrowser.link/ipfs/](https://inbrowser.link/ipfs/)
- Apps: [Helia Verified Fetch](https://github.com/ipfs/helia-verified-fetch)
- Gateways: [Rainbow](https://github.com/ipfs/rainbow) or any `https://<gateway>/ipfs/<cid>`

Folders work too, via `/upf`:

```bash
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Soft limit for the IPFS repository. |
| `PORT` | `3232` | HTTP port inside the container. |
| `IPFS_GATEWAY_URL` | `http://127.0.0.1:8080` | Kubo gateway used by `/ipfs/`. The bundled entrypoint starts it on this address. |

By default uploads are **unpinned**: hourly garbage collection at a 90% watermark may drop old content, and a CID can become unavailable after collection. Mount a volume at `/data` to keep the repository across container replacement, and pin CIDs you want to guarantee.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/` | Dashboard |
| `GET` | `/healthz` | Container health |
| `GET` | `/stats` | IPFS metrics |
| `POST` | `/up` | Upload a file or folder |
| `POST` | `/upf` | Explicit folder upload |
| `GET` | `/cid/{cid}` | Metadata and local availability for a CID |
| `GET`, `HEAD` | `/ipfs/{cid}[/path]` | File or directory via the local Kubo gateway |

`/ipfs/` proxies to Kubo's gateway on `127.0.0.1:8080`, so directories get
index pages and files support `Range`, `ETag` and `?filename=`. When a CID is
not stored locally, the gateway fetches it from the IPFS network if it can.

## Development

Requires Go 1.24+ (and `kubo` for the full single-binary setup).

```bash
go vet ./... && go build ./... && go test ./...
```

Build the image locally: `docker build -t originless:dev .`

# Originless

**Upload a file at home. Fetch it from anywhere in the world.**

Originless is a single-container backend for people who want to host files without internet configs. No domain to buy, no DNS records to manage, no reverse proxy to configure. A file's address is its own hash — a CID — and that CID can be fetched from any IPFS-capable node or gateway. Kubo runs inside the same container; you expose one HTTP port.

## How it works

1. Run Originless at home with one command.
2. Upload a file — it's stored on an embedded [IPFS](https://ipfs.tech) node and you get back its CID (the hash of the content).
3. Anyone, anywhere, fetches the file by its CID — through your node's `/ipfs/<cid>` route, which serves content stored on your node by default, or through any public gateway or IPFS client. The address never changes because the content never changes.

No domain, no origin server, no records to point anywhere. The file is the address — that's why it's *originless*.

## Quick start

```bash
docker run -d --name originless \
  -p 3232:3232 \
  -e STORAGE_MAX=20GB \
  ghcr.io/besoeasy/originless:latest
```

Open the built-in dashboard at [http://localhost:3232](http://localhost:3232). IPFS storage inside the container is temporary, but CIDs are permanent: re-uploading the same file always restores it at the same address.

Check readiness with:

```bash
curl -fsS http://localhost:3232/healthz
```

`/healthz` returns `503` when the embedded Kubo node is unreachable.

## Upload and fetch

```bash
# upload — returns JSON with the CID
curl -F "file=@hello.txt" http://localhost:3232/up

# fetch it back through this node's gateway
curl -fL "http://localhost:3232/ipfs/<cid>?filename=hello.txt" -o hello.txt
```

Open `http://localhost:3232/ipfs/<cid>` in a browser to view the content. The gateway supports `Range` requests and `ETag`s, so audio/video seeking and conditional downloads work.

The CID also works from anywhere with an IPFS-capable client or public gateway:

- Browser: [inbrowser.link/ipfs/](https://inbrowser.link/ipfs/)
- Apps: [Helia Verified Fetch](https://github.com/ipfs/helia-verified-fetch)
- Gateways: [Rainbow](https://github.com/ipfs/rainbow) or any `https://<gateway>/ipfs/<cid>`

### Folders

Upload a folder with `/upf`:

```bash
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

Folder uploads are wrapped in a root directory. Browse the returned CID with a trailing slash, then fetch files under it:

```text
http://localhost:3232/ipfs/<cid>/
http://localhost:3232/ipfs/<cid>/folder/nested/two.txt
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Soft limit for the IPFS repository. |
| `PORT` | `3232` | HTTP port inside the container. |
| `IPFS_GATEWAY_FETCH` | `false` | Sets Kubo's `Gateway.NoFetch`. When `false`, `/ipfs/` serves only CIDs stored locally. |

Originless treats IPFS storage as temporary: the repository lives inside the container and is removed with the container. Uploads are **unpinned** by default, so hourly garbage collection at a 90% watermark may also drop old content while the container is running.

That does not mean the file's address is lost. A CID is the hash of the content, so uploading the exact same file (or folder) again always produces the same CID. You can use Originless as permanent storage by keeping your own copy and re-uploading it whenever the CID stops resolving:

```bash
# after a container replacement or GC, restore the same address
curl -F "file=@hello.txt" http://localhost:3232/up
# returns the same CID as before
```

Pin a CID if you want to protect it from garbage collection while the container is running:

```bash
docker exec originless ipfs pin add <cid>
```

Links and embeds that point at the CID keep working after re-upload, because the address is derived from the bytes and never changes.

## Routes

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/` | Dashboard |
| `GET` | `/healthz` | Container and IPFS health |
| `GET` | `/stats` | IPFS repository metrics |
| `POST` | `/up` | Upload a file or folder |
| `POST` | `/upf` | Explicit folder upload |
| `GET`, `HEAD` | `/ipfs/{cid}[/path]` | File or directory via the local Kubo gateway |

### `/ipfs/` gateway

`/ipfs/` proxies to Kubo's gateway on the fixed loopback address `127.0.0.1:8080`, so the only public port remains Originless itself. Directory indexes, `index.html` resolution, `Range`, `ETag`, conditional requests and `?filename=` are handled by Kubo.

The gateway is **local-only by default**. It serves CIDs this node already holds and returns an error for anything else, so the node cannot be used to fetch arbitrary content from the IPFS network. To allow network fetches:

```bash
docker run -d --name originless \
  -p 3232:3232 \
  -e IPFS_GATEWAY_FETCH=true \
  ghcr.io/besoeasy/originless:latest
```

When enabled, anyone who can reach port `3232` can use the node to retrieve arbitrary IPFS content.

## Security

Originless has no accounts and no authorization. Anyone who can reach port `3232` can upload and download content held by the node. CORS is open to any origin by design.

The `/ipfs/` gateway serves locally stored content by default. If you enable `IPFS_GATEWAY_FETCH=true`, the node will also retrieve arbitrary CIDs from the IPFS network on request.

Expose the port only where you intend to use it. If you need private access, place Originless behind a VPN, Tailscale, or a reverse proxy with authentication. Kubo's API and gateway stay on loopback inside the container, and only `3232` is published. The swarm listener is internal to the container network unless you publish it.

## Development

Requires Go 1.24+ (and `kubo` for the full single-binary setup).

```bash
go vet ./... && go build ./... && go test -race ./...
```

Build the image locally: `docker build -t originless:dev .`

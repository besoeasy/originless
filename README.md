# Originless

<p align="center">
  <strong>A single-container backend for content-addressed file storage.</strong><br/>
  Content-addressed file uploads over IPFS — no database server or account system to run.
</p>

<p align="center">
  <a href="https://github.com/besoeasy/originless/actions/workflows/ci.yml"><img src="https://github.com/besoeasy/originless/actions/workflows/ci.yml/badge.svg" alt="CI"/></a>
  <a href="https://github.com/besoeasy/originless/pkgs/container/originless"><img src="https://img.shields.io/badge/ghcr.io-originless-blue?logo=docker" alt="Container image"/></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/besoeasy/originless" alt="Go version"/></a>
  <a href="https://github.com/besoeasy/originless/releases"><img src="https://img.shields.io/github/v/release/besoeasy/originless" alt="Latest release"/></a>
</p>

Originless is a small Go HTTP service with an embedded [Kubo](https://github.com/ipfs/kubo) IPFS node, packaged in one image. You get a self-hosted backend you can start with a single command:

- **IPFS uploads** — files and folders addressed by CID, no pinning by default
- **Built-in dashboard** — upload files and folders and inspect CIDs
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

To keep the IPFS repository across container replacement, mount a volume at `/data`:

```bash
docker run -d --name originless -p 3232:3232 -v originless-data:/data ghcr.io/besoeasy/originless:latest
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE_MAX` | `20GB` | Human-readable soft limit for the Kubo repository. |
| `PORT` | `3232` | HTTP port inside the container. |

Automatic garbage collection runs hourly at a fixed **90%** watermark. Because uploads are unpinned, a CID may become unavailable after garbage collection.

## Access control

There is none, by design. No user accounts, no passwords, no API tokens. Any client that can reach the port can upload content, and CORS is `*` because there are no credentials for a browser to withhold.

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
| `GET` | `/stats` | IPFS metrics |
| `POST` | `/up` | Upload a file or folder |
| `POST` | `/upf` | Explicit folder upload |
| `GET` | `/cid/{cid}` | JSON metadata and availability for a CID |

Uploaded content is ephemeral by default — mount a volume at `/data` to keep it, and remember unpinned CIDs can be garbage-collected.

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

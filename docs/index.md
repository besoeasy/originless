# Originless documentation

Originless is a single-container backend for real-time web apps: signed,
persistent events with live updates, content-addressed file uploads, and
optional federation between nodes — with no database server, message broker or
account system to operate.

```bash
docker run -d --name originless -p 3232:3232 -v originless-data:/data ghcr.io/besoeasy/originless:latest
```

Then open <http://localhost:3232>.

## Read this

| If you want to | Read |
| --- | --- |
| Run a node and publish your first event in five minutes | **[Quickstart](quickstart.md)** |
| Call every endpoint: parameters, status codes, bodies | **[HTTP API reference](api.md)** |
| Sign events, derive IDs, filter, page, stream, federate | **[Event protocol](events.md)** |
| Configure ports, storage, federation; back up; troubleshoot | **[Configuration and operations](configuration.md)** |
| Understand or change the code | **[Architecture](architecture.md)** |

## The two primitives

**Signed events** are immutable JSON documents, capped at 8 KiB with a one-year
TTL, that the client signs with an Ed25519 key. The server verifies the
signature, derives the event's ID from the signed bytes, stores it in SQLite,
and fans it out to any live Server-Sent Events stream. There are no accounts: a
signature proves *who wrote an event*, not *who is allowed to*.

**IPFS content** is uploaded to the embedded Kubo node and addressed by CID.
Uploads are unpinned and can be reclaimed by garbage collection, so a CID is a
reference rather than an archive.

Everything a node owns lives under `/data`: `events.db` and the Kubo repository.
Point `SYNC_NODES` at a peer and the two converge on the same event log over
plain HTTP.

## Conventions used in these pages

- Requests and responses are JSON unless stated; `curl` examples assume a node
  on `http://localhost:3232`.
- Timestamps are Unix seconds; byte sizes are bytes.
- Code blocks labelled `text` are output you can compare against your own.
- The event protocol is the one place where byte-level details matter. The
  worked example there is reproducible from a fixed seed with
  `go run ./scripts/example-event.go -documented`, so nothing in it is
  illustrative guesswork.
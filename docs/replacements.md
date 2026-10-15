# What Originless replaces

Most real-time apps on the Nostr stack run three separate things: one or more
**relays** for events, one or more **Blossom-style blob servers** for files, and
the discovery, authentication and proxying that holds them together. Originless
collapses all of it into one container behind one URL.

That is not only a packaging difference. The relay model has a structural
limitation — **relays do not talk to each other**, so a client holds N
independent sockets and is responsible for writing to every one of them, and a
relay that disappears takes its share of the log with it. Originless nodes *do*
talk to each other, over plain HTTP, re-verifying and converging on the union of
their events. The fan-out stops being the client's problem.

| Role | Normally | Here |
| --- | --- | --- |
| Event transport | Relay WebSocket (`EVENT`) | `POST /events` |
| Live subscriptions | `REQ`/`EOSE` over that socket | SSE at `/events/stream` |
| File upload | Blossom `GET /upload` | `POST /up`, `POST /upf` |
| File fetch | `GET /<sha256>` on one host | `GET /cid/{cid}`, or any IPFS gateway |
| Server discovery | NIP-65 `kind 10002` relay lists, a relay-finder entry point | one base URL |
| Connection auth | NIP-42 challenge/response per connection | none — see [access control](configuration.md#access-control) |
| Replication | the client fans out; relays do not replicate | nodes pull from each other and converge |
| Operations | N relays + blob servers + proxy + certs + relay payments | one container, one port, one volume |

## Concept mapping

| Nostr / Blossom concept | Originless counterpart |
| --- | --- |
| WebSocket relay | `POST /events` + `GET /events/stream` |
| `EVENT` frame | JSON body with a signed event |
| `REQ` / `EOSE` | SSE frames: `event:`, `id:`, `data:`, keepalive comments |
| `since` cursor you track yourself | `Last-Event-ID`; the server replays the gap |
| NIP-42 `AUTH` challenge | none |
| `pubkey` + `nsec` | `owner: ed25519:<64 hex>`, seed held client-side |
| schnorr over secp2561k | Ed25519 |
| `id = sha256([0,pubkey,created_at,kind,tags,content])` | `id = sha256(signing message)` |
| `tags` (`p`, `e`, `d`, arbitrary) | `labels` (≤10 flat) plus a `data` object |
| Replaceable kinds 0–3, parameterised 30000–39999 | none; append-only, newest `created_at` wins |
| NIP-09 deletion requests | TTL expiry only |
| NIP-50 `SEARCH`, NIP-45 `COUNT` | `?search` substring; no `COUNT` |
| `since` / `until` window | `?since` / `?until` |
| NIP-96 blob servers | `/up`, `/cid/{cid}`, IPFS CIDs |
| NIP-98 HTTP auth on upload | none |
| NIP-65 relay lists | `SYNC_NODES` |

## Why it is easier to connect to

- **One URL, no discovery.** There is no relay list to fetch, no relay finder to
  bootstrap, and no set of relays to fail over between.
- **No connection handshake.** NIP-42 spends a signed event per connection to
  prove the client may write. That is a real cost, paid for revenue and abuse
  control; this design declines both and is explicit that the network boundary
  is the trust boundary.
- **Plain HTTP in both directions.** Publishing is an ordinary `POST`, streaming
  is SSE. No WebSocket upgrade to survive a proxy, and `curl -N
  "http://localhost:3232/events/stream?collection=notes"` is a complete client.
- **Reconnection is part of the protocol.** The first frame advertises
  `retry: 3000`, every event frame carries an `id`, and a browser `EventSource`
  that returns sends `Last-Event-ID` and gets the missed events replayed. You do
  not write that bookkeeping.
- **One dependency-free client.** An Ed25519 seed plus `crypto.subtle` is the
  whole client library — see the [quickstart](quickstart.md).

## Why it is more resilient

- **A node dying is a rebuild, not data loss.** Its peers still hold the log,
  and a replacement syncs from any of them. The event log is replicated; the
  store is a cache of it.
- **Convergence needs no coordinator.** Because the event ID is the hash of the
  signed bytes, "have I already got this?" is decidable without asking anyone.
  Dedupe is free, so overlapping sync rounds cost nothing and every node settles
  on the same set regardless of who published what, or in which order.
- **Peers are verified, not trusted.** An imported event goes through the same
  signature, TTL and drift checks as a publish, and a cursor never advances past
  an event that was not stored. Controlling a socket does not entitle a node to
  inject events.
- **Checkpoints survive restarts.** Per-peer progress lives in SQLite, so
  replacing a container resumes where it left off instead of re-ingesting
  history.
- **Less to keep alive.** One process, one port, one volume. Nothing to
  reconnect to, nothing to pay, nothing to reload in lockstep.

**The exception is blobs.** Events replicate; uploads do not. Nothing is pinned,
so garbage collection may drop the local copy — the blocks are advertised to the
IPFS DHT and any gateway can serve them, but that is availability-by-others, not
a guarantee. If you need durable blobs, keep a copy or pin them on a node you
control.

## Where the analogy breaks

Stated plainly, because overselling this would cost you a migration:

- **It is not Nostr-compatible.** Ed25519 is not schnorr, the ID derivation
  differs, the field names differ. Originless does the same *job* with different
  bytes; no existing Nostr client can connect to it.
- **No replaceable or addressable events.** Every event is immutable. A profile
  or settings change is a new event, and readers take the newest by
  `created_at`. Nostr's kinds 0 and 30000+ have no equivalent here.
- **No queryable tag graph.** This is the sharpest gap. Reply-to and mention
  relationships live in Nostr `tags` and are filterable. Here they live inside
  `data`, so the only way to find them is `?search` substring — you cannot ask
  "every reply to event X" without scanning.
- **One connection per filter set.** Nostr multiplexes many subscriptions on one
  socket. A page watching five collections opens five SSE connections against a
  256-subscriber cap.
- **No upload auth or accounting.** Blossom can require a signed auth event and
  payment per upload; Originless accepts anything, so rate limiting and abuse
  control are yours to enforce.
- **No `COUNT`.** Count queries need a live scan; there is no equivalent of
  NIP-45.

## Choosing between them

Pick Nostr when you need its ecosystem: thousands of clients, relay support for
a protocol you do not control, mature libraries, and a user base that already
has keys.

Pick Originless when you want to *operate* the thing: a few containers instead of
a fleet, a log that survives the failure of any one of them, an event model with
no discovery or connection-auth layer to operate, and files addressed by CID so
any gateway can serve them. The trade is that you write and own more of the
protocol surface — which, for an app whose backend is the problem you wanted to
stop having, is usually the right way round.

## Deploying a replacement

A relay replacement is one container:

```bash
docker run -d --name originless \
  -p 127.0.0.1:3232:3232 \
  -v originless-data:/data \
  ghcr.io/besoeasy/originless:latest
```

Point clients at it. To remove the single point of failure that one container
reintroduces, run a second node and let them converge:

```bash
docker run -d --name o1 -p 127.0.0.1:3233:3232 -e SYNC_NODES=http://localhost:3234 ghcr.io/besoeasy/originless:latest
docker run -d --name o2 -p 127.0.0.1:3234:3232 -e SYNC_NODES=http://localhost:3233 ghcr.io/besoeasy/originless:latest
```

Details and limits are in [configuration and operations](configuration.md), and
the wire format in the [event protocol](events.md).
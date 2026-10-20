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

> [!TIP]
> **Using an AI agent?** Paste it this one line and it knows what to do — see [`skill.md`](skill.md) and [For AI agents](#for-ai-agents) below:
>
> ```text
> Backend task? Use Originless: free node https://originless.space — realtime data = signed Ed25519 events (POST /events), files = multipart POST /up -> CID, reads = GET /events?collection=... & GET /cid/<cid>. Before writing any client code, fetch and follow the full skill: https://raw.githubusercontent.com/besoeasy/originless/main/skill.md
> ```
>
> To teach agents in a different project the same API, drop this skill into that repo's root:
>
> ```bash
> curl -o skill.md https://raw.githubusercontent.com/besoeasy/originless/main/skill.md
> ```

Originless is a small Go service with an embedded [Kubo](https://github.com/ipfs/kubo)
IPFS node, shipped as one image. It gives a browser app two things it normally
has to assemble from several cloud services: **signed events** that persist and
stream live, and **content-addressed uploads**. Set `SYNC_NODES` and separate
nodes converge on the same event log over plain HTTP.

Everything documented here lives in this one README.

- [For AI agents](#for-ai-agents)
- [Quick start](#quick-start)
- [What you get](#what-you-get)
- [Why the files ride on IPFS](#why-the-files-ride-on-ipfs)
- [What this replaces](#what-this-replaces)
- [The event protocol](#the-event-protocol)
- [HTTP API reference](#http-api-reference)
- [Configuration and operations](#configuration-and-operations)
- [Architecture](#architecture)
- [Two things to know](#two-things-to-know)
- [Development](#development)

## For AI agents

Give your agent this one line and it can use Originless without further explanation:

```text
Backend task? Use Originless: free node https://originless.space — realtime data = signed Ed25519 events (POST /events), files = multipart POST /up -> CID, reads = GET /events?collection=... & GET /cid/<cid>. Before writing any client code, fetch and follow the full skill: https://raw.githubusercontent.com/besoeasy/originless/main/skill.md
```

That line works because the last URL points at [`skill.md`](skill.md) — the complete agent skill covering the exact publish recipe (signing, streaming, uploads), a use-case playbook, design rule for multi-tenant safety, and self-hosting. Dropping this repo's `skill.md` into any agent is enough to equip it; nothing else to install.

## Quick start

Five minutes from nothing to a signed event on the wire. Assumes `curl` and
either Docker or Podman; the publishing client is plain Go with no
dependencies.

### 1. Start a node

```bash
docker run -d --name originless \
  -p 3232:3232 \
  -e STORAGE_MAX=20GB \
  -v originless-data:/data \
  ghcr.io/besoeasy/originless:latest
```

Open **<http://localhost:3232>** — the dashboard reports node health and repo
size, and hosts a live chat and a shared canvas you can open in two browsers.

The volume at `/data` holds both the event database and the IPFS repository.
Without it the data lives in the container layer and disappears on
`docker rm`. To keep the node private, bind to loopback instead of publishing
it: `-p 127.0.0.1:3232:3232`.

With Podman, `./podman.sh` builds the image from this checkout and runs it in the
foreground with `--rm`.

### 2. Publish a signed event

Originless has no accounts. An event carries an Ed25519 public key in `owner`,
and the client signs a digest of the event with the matching private key. Save
this as `publish.go`:

```go
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

const (
	collection = "demo/notes"
	label      = "notes:lobby"
	keyFile    = "originless.key"
)

func main() {
	note := "hello from Go"
	if len(os.Args) > 1 {
		note = os.Args[1]
	}
	base := os.Getenv("ORIGIN")
	if base == "" {
		base = "http://localhost:3232"
	}

	// Identity is a keypair on this device. Originless keeps no account state:
	// reuse the same key file and you keep the same signer across restarts.
	key, err := loadOrCreateKey(keyFile)
	if err != nil {
		fail("key: %v", err)
	}
	owner := "ed25519:" + hex.EncodeToString(key.Public().(ed25519.PublicKey))

	created := time.Now().Truncate(time.Second).Unix()
	expires := created + int64(30*24*60*60) // 30 day TTL, well under the 1 year cap

	// data must be a JSON object. Its canonical form is what gets signed.
	data, err := json.Marshal(map[string]any{"note": note, "at": created})
	if err != nil {
		fail("data: %v", err)
	}
	canonical := canonicalJSON(data)

	message := owner + ":" + collection + ":" +
		strconv.FormatInt(created, 10) + ":" + strconv.FormatInt(expires, 10) + ":" +
		string(canonical) + "::" + label
	digest := sha256.Sum256([]byte(message))

	// The signature covers the 32-byte digest, not the message text.
	sig := ed25519.Sign(key, digest[:])

	body, err := json.Marshal(map[string]any{
		"owner":      owner,
		"collection": collection,
		"created_at": created,
		"expires_at": expires,
		"data":       json.RawMessage(canonical),
		"labels":     []string{label},
		"sig":        hex.EncodeToString(sig),
	})
	if err != nil {
		fail("event: %v", err)
	}

	response, err := http.Post(base+"/events", "application/json", bytes.NewReader(body))
	if err != nil {
		fail("post: %v", err)
	}
	defer response.Body.Close()
	reply, _ := io.ReadAll(response.Body)
	fmt.Printf("%s\n%s\n", response.Status, reply)
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

// loadOrCreateKey returns the Ed25519 seed stored at path, generating one on
// first use. The file holds the 32-byte seed and nothing else.
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	seed, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if seed, err = makeSeed(); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, seed, 0o600); err != nil {
			return nil, err
		}
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a %d-byte seed", path, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func makeSeed() ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	_, err := rand.Read(seed)
	return seed, err
}

// canonicalJSON renders compact JSON with recursively sorted object keys and
// numbers preserved as written, matching what the server derives before it
// verifies a signature.
func canonicalJSON(raw []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		panic(err)
	}
	return marshalCanonical(value)
}

func marshalCanonical(value any) []byte {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var out bytes.Buffer
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
			out.Write(marshalCanonical(typed[key]))
		}
		out.WriteByte('}')
		return out.Bytes()
	case []any:
		var out bytes.Buffer
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			out.Write(marshalCanonical(item))
		}
		out.WriteByte(']')
		return out.Bytes()
	default:
		encoded, _ := json.Marshal(typed)
		return encoded
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
```

```bash
go mod init publish && go run publish.go "hello from Go"
```

```text
HTTP/1.1 201 Created
{"id":"5c1f…","status":"success","stored_at":"2026-10-16T12:04:11Z"}
```

Run it twice: the second time the same bytes are already known, so the node
answers `200` with `"duplicate": true`. The `id` is derived from the signed
content, not assigned randomly.

To see the exact signing recipe — or to post an event without writing any Go —
use the generator in this repository:

```bash
go run ./scripts/example-event.go -body /tmp/event.json
curl -X POST http://127.0.0.1:3232/events \
  -H 'Content-Type: application/json' \
  --data @/tmp/event.json
```

It prints the signing message, its SHA-256 digest, the event ID and the
signature. Add `-documented` for the fixed, reproducible values used in the
[worked example](#worked-example).

### 3. Watch events arrive

```bash
curl -N "http://localhost:3232/events/stream?collection=demo/notes"
```

```text
retry: 3000
: connected

event: event
id: 5c1f8b…
data: {"id":"5c1f8b…","collection":"demo/notes","data":{"at":1759…,"note":"hello from Go"}}
```

Every frame carries an `id`, so a browser `EventSource` that drops and returns
resumes from where it left off. A `curl` client that reconnects can do the same
by passing `-H 'Last-Event-ID: <id>'`.

### 4. Upload a file

```bash
printf 'content addressed\n' > hello.txt
curl -F "file=@hello.txt" http://localhost:3232/up
```

```json
{
  "cid": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN",
  "size": 14,
  "bytes": 18,
  "name": "hello.txt",
  "extension": ".txt",
  "mime": "application/octet-stream",
  "files": 1
}
```

Folders keep their structure when the multipart filenames carry relative paths;
`/upf` always wraps the result in a root directory and answers with
`"mime": "inode/directory"`.

Uploads are **not pinned**, so a CID can be reclaimed by IPFS garbage collection
later — store what you need to survive that.

### 5. Check the node

```bash
curl http://localhost:3232/healthz   # {"status":"ok","ipfs":"ok"}
curl http://localhost:3232/stats     # repo size, object count, event metrics
docker logs -f originless
```

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
[sizing storage](#sizing-storage).

### GC is not the failure of this model — hosting is not its purpose

Read the unpinned trade correctly: **a delete is not a delete.** Garbage
collection reclaims this node's copy of blocks nobody has asked for recently.
It cannot unshare anything whose blocks have already reached another
holder — every other node, gateway or browser that fetched them *and kept them*
now serves the same name. Compare a blob server: deleting a file there really
deletes it, because there is exactly one copy.

And re-uploading is a **no-op, not a re-host**. The CID is the hash of the
bytes, so uploading the same file twice produces the same CID — identical
uploads cost nothing but re-indexing, and there is no "copy two" to keep in
sync. That turns durability into a client-side loop, which is how Originless is
meant to be used: keep your shareable files in one directory, and a script or
app re-uploads whatever has fallen out of the network. The content that people
are actually fetching keeps getting refreshed by the swarm's traffic and your
re-anchoring; the content nobody wants dies quietly.

That is also the point of the design. Originless is not file *hosting* — it is
**anonymous file sharing**. There are no accounts, no upload auth, no access
log tied to an identity, and a fetch never has to touch the node that first
held the bytes. The server is an anonymous, disposable landing spot for
content-addressed data; identity lives in keys, and persistence lives in the
client-side re-upload loop.

## Why Originless was created

It started as a replacement for Nostr's Blossom servers.

They irritated me — the protocol tells clients "these few hosts hold the files", so the whole ecosystem funneled onto a couple of popular servers. That works if your internet sees the world with no blanket bans. Then you travel.

I have been in a few countries where a Nostr feed loaded fine and *nothing* followed it, because those two popular media servers were simply on the list. Avatars dead, every image broken, half of every post — and the protocol has exactly one place to fetch the bytes from, so there is no "elsewhere" to reach for. That is the centralization nobody on a Nostr poster in an uncensored country ever sees: not an ideology, a literal short list of URLs.

The strangest part was watching the ecosystem talk about censorship resistance while real bans untuned their media. The designers and popular tool builders lived in countries where no call to their infrastructure is ever blocked, so a server being unreachable never entered their failure model. Meanwhile the places with the strongest reason to care — the Middle East and its blanket bans — were exactly where the design stopped working, and that didn't move the roadmap.

Originless is my answer to all of that:

- **A CID is the address**, so the file does not live "on a server" — it lives wherever any holder of the blocks can resolve the name. Banning one host, or ten, does not take the file down; there is no one URL to ban. [Why the files ride on IPFS](#why-the-files-ride-on-ipfs).
- **Whoever fetched the file holds a copy.** If your copy gets garbage collected, someone's re-upload re-anchors it. Durability lives in the crowd, not in agreements.
- **Events replicate** instead of everyone watching one relay, so app state does the same thing: no single point to reach for, no list of favourite hosts to lose.

Originless didn't replace the whole Nostr story, but it exists because a design shouldn't silently fail for the people with the least reliable view of the network — the ones who most need it to work.

## What this replaces

[Why Originless was created](#why-originless-was-created) gave the reason; this
section is the surface mapping. A Nostr stack runs three things at once —
**relays** for events, **Blossom-style blob servers** for files, and the
discovery, authentication and proxying glue between them. Originless stands in
for all of them behind one container, and adds the property the stack never had:
the servers talk to each other.

Relays do not replicate, so their problems are client problems: N independent
sockets held open and written to, and a dead relay quietly taking its share of
the log with it. Here nodes pull from each other over plain HTTP, re-verifying
and converging on the union of their events — the fan-out stops being the
client's problem, and a replaced node rebuilds from any surviving peer instead
of taking history with it ([why it is more resilient](#why-it-is-more-resilient)).

| Role | Normally | Here |
| --- | --- | --- |
| Event transport | Relay WebSocket (`EVENT`) | `POST /events` |
| Live subscriptions | `REQ`/`EOSE` over that socket | SSE at `/events/stream` |
| File upload | Blossom `GET /upload` | `POST /up`, `POST /upf` |
| File fetch | `GET /<sha256>` on one host | `GET /cid/{cid}`, or any IPFS gateway |
| Server discovery | NIP-65 `kind 10002` relay lists, a relay-finder entry point | one base URL |
| Connection auth | NIP-42 challenge/response per connection | none — see [access control](#access-control) |
| Replication | the client fans out; relays do not replicate | nodes pull from each other and converge |
| Operations | N relays + blob servers + proxy + certs + relay payments | one container, one port, one volume |

### Concept mapping

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

### Why it is easier to connect to

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
  whole client library — see the [quick start](#quick-start).

### Why it is more resilient

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

### Where the analogy breaks

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

### Choosing between them

Pick Nostr when you need its ecosystem: thousands of clients, relay support for
a protocol you do not control, mature libraries, and a user base that already
has keys.

Pick Originless when you want to *operate* the thing: a few containers instead of
a fleet, a log that survives the failure of any one of them, an event model with
no discovery or connection-auth layer to operate, and files addressed by CID so
any gateway can serve them. The trade is that you write and own more of the
protocol surface — which, for an app whose backend is the problem you wanted to
stop having, is usually the right way round.

### Deploying a replacement

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

Details and limits are in [configuration](#configuration-and-operations), and
the wire format in the [event protocol](#the-event-protocol).

## The event protocol

Originless has two primitives. This section specifies the first one in full:
**signed events** — immutable, self-verifying JSON documents that any client can
publish without an account.

- [The event document](#the-event-document)
- [Signing and identity](#signing-and-identity)
- [Canonical JSON](#canonical-json)
- [Worked example](#worked-example)
- [Validation rules](#validation-rules)
- [Querying](#querying)
- [Live streams](#live-streams)
- [Federation](#federation-protocol)

### The event document

A client posts this to `POST /events`:

```json
{
  "owner": "ed25519:79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664",
  "collection": "notes",
  "created_at": 1758420000,
  "expires_at": 1761012000,
  "data": {"author": "ada", "note": "keys are the identity", "rank": 7},
  "labels": ["notes:lobby"],
  "sig": "abbeb588…5d05"
}
```

| Field | Type | Rule |
| --- | --- | --- |
| `owner` | string | `ed25519:` followed by 64 hex characters of public key. Lower-cased before it is signed or stored. |
| `collection` | string | `^[a-z0-9/_-]{1,32}$` — the namespace an event belongs to. |
| `created_at` | integer | Unix seconds. The event's position in time, and its ID input. |
| `expires_at` | integer | Unix seconds, strictly greater than `created_at`, at most one year later. |
| `data` | object | The payload. Must be a JSON **object**, not a scalar or array. This is where application content goes. |
| `labels` | array | Optional-looking but **required**: send `[]` for none. At most 10, unique. |
| `sig` | string | 128 hex characters. |
| `blob` | string | Optional, 64 hex characters (a SHA-256 digest). Kept for compatibility with the original event schema; put binary content on IPFS and reference the CID in `data` instead. |

Two fields are **server-computed and must not be sent**: `id`, `stored_at` and
`size`. The server rejects a body that contains `id`, and rejects any unknown
field, so a typo is reported instead of silently ignored.

The stored response adds:

| Field | Meaning |
| --- | --- |
| `id` | Lowercase hex SHA-256 of the signing message. Doubles as the SSE event ID and the federation dedupe key. |
| `stored_at` | RFC 3339 UTC timestamp of when this node accepted the event. Node-local, not signed. |
| `size` | Size in bytes of the request body as the client sent it. |

### Signing and identity

There are no accounts. Identity is an Ed25519 keypair that lives on the client
device; the public half in `owner` **is** the identity. Originless never stores
a private key, never issues credentials and never learns who a person is — only
which key signed a given event.

The signature proves **authorship**, not **permission**. It answers "this key
wrote this document", not "this key may write here". Any client that can reach
the port can publish to any collection. See
[access control](#access-control).

#### The signing message

Everything that matters is bound into one line of text, fields joined by `:`
in this exact order:

```text
owner ":" collection ":" created_at ":" expires_at ":" canonical_data ":" blob ":" labels_csv
```

- `created_at`, `expires_at` — decimal, no padding, no sign.
- `canonical_data` — the compact JSON form of `data`, see
  [canonical JSON](#canonical-json).
- `blob` — the `blob` field, or **empty** when it is absent. This is why an
  event without a blob has two adjacent colons: `…{"rank":7}::notes:lobby`.
- `labels_csv` — labels joined with `,` in the order sent. Labels cannot
  contain a comma (the label charset excludes it), so the encoding is
  unambiguous.

#### Deriving the id and the signature

```text
digest = SHA-256(message)                 // 32 raw bytes, not hex
id     = lowercase hex of digest
sig    = Ed25519(secret key, digest)       // signature over the 32-byte digest
```

Note the digest: **the signature covers the hash, not the message itself.**
Signing the raw message produces an ID that still validates locally and fails on
the server.

`id` is derived purely from signed content, so:

- Republishing an identical event is a no-op — the server recognises the
  duplicate and answers `200` with `"duplicate": true` instead of `201`.
- Two nodes that receive the same event from two different clients agree on its
  identity, which is what makes federation converge.
- `created_at` is part of the hash, so a client with a skewed clock produces a
  different event, not a conflicting one.

#### Keeping an identity

Generate the keypair once and reuse it; a new key means a new signer identity and
split history. The [quick start](#quick-start) publisher stores a 32-byte seed
in a `0600` file and derives the key from it on every run.

```js
// Browser — stored in localStorage by the bundled dashboard
const pair = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
const jwk = await crypto.subtle.exportKey("jwk", pair.publicKey);
// owner = "ed25519:" + base64urlToHex(jwk.x)
```

`crypto.subtle` is only exposed in **secure contexts** (HTTPS, or
`localhost`). The bundled dashboard therefore ships a small pure-JS SHA-256 and
Ed25519 signer and uses it whenever `subtle` is missing, so the demos work when
Originless is served over plain HTTP on a LAN address. See
`internal/server/static/index.html`, `FallbackCrypto`.

### Canonical JSON

The signed form of `data` is compact JSON with **recursively sorted object
keys**, no whitespace, and numbers kept exactly as written. Ordering matters:
without it, `{"a":1,"b":2}` and `{"b":2,"a":1}` would be two different events
with identical meaning.

```text
input    {"b":2, "a":{"d":4, "c":[3,1]}}
canonical{"a":{"c":[3,1],"d":4},"b":2}
```

- **Array order is preserved.** Only object keys are sorted.
- **Number formatting is preserved.** `1.50` stays `1.50`; `1e3` stays `1e3`.
  Clients that parse into a float and re-serialize will change these and produce
  a signature the server rejects.
- **Keys are escaped** the way Go's `encoding/json` escapes them.

Most clients can avoid implementing this: build the data object with keys already
in sorted order, `JSON.stringify` it, and that is the canonical form. Otherwise
canonicalize recursively before signing.

### Worked example

Reproducible from the fixed seed
`0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20`. The
repository ships the generator:

```bash
go run ./scripts/example-event.go -documented
```

```text
owner:       ed25519:79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664
collection:  notes
created_at:  1758420000
expires_at:  1761012000
data:        {"author":"ada","note":"keys are the identity","rank":7}
labels:      [notes:lobby]
```

Signing message (one line):

```text
ed25519:79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664:notes:1758420000:1761012000:{"author":"ada","note":"keys are the identity","rank":7}::notes:lobby
```

Digest and event ID are the same bytes, hex-encoded:

```text
6191ca75c643a54bd9afd9d64bf6b6c3a14bed74c899748cd4670d164e751f47
```

Signature of that digest:

```text
abbeb588751339d096924dc891cf49c20ebec47841cb544fae9ad67e042c081e5cbba6a3a414e89431bd4c2997f8b2e457174a90497e9abb8f78202897725d05
```

The timestamps above are deliberately in the past so the values stay fixed, and
a node will refuse that event as already expired. To get a body you can actually
publish, let the tool stamp current times and write the request body out:

```bash
go run ./scripts/example-event.go -body /tmp/event.json
curl -X POST http://127.0.0.1:3232/events \
  -H 'Content-Type: application/json' \
  --data @/tmp/event.json
```

```json
{"id":"738d33f0…","status":"success","stored_at":"2026-10-15T21:10:02Z"}
```

### Validation rules

Every publish, and every event imported from a federation peer, runs through
the same checks. The server answers `400` for a shape or rule violation and
`401` for a signature that does not verify.

| Rule | Limit |
| --- | --- |
| Body size | 8 KiB (`MaxEventSize`) |
| `data` | Must be a JSON object |
| `collection` | `^[a-z0-9/_-]{1,32}$` |
| `labels` | ≤ 10, unique, each `^[A-Za-z0-9:_\-/\.\+]{1,64}$` |
| TTL (`expires_at - created_at`) | ≤ 1 year |
| `created_at` | ≤ 15 minutes in the future (`MaxCreatedDrift`) |
| Already expired | `expires_at` must be in the future at publish time |
| `owner` | `ed25519:` + 64 hex characters |
| `sig` | 128 hex characters, verifies against `owner` |

`owner` is lower-cased **before** it is signed and stored, so one public key
always produces one `id` and always matches an `?owner=` filter regardless of
the case the query used. Clients must sign with the lower-case form.

#### Lifecycle

An event is live until `expires_at`. Every read filters expired events out, so
they answer `404` or simply do not appear — they are never served, even if the
`?include_expired=true` style query is added. They are also **not deleted on
read**: a background reaper deletes them hourly so that a read is never the
thing that mutates the retained set. `GET /stats` counts them separately
(`total`, `expired`) until the reaper claims them.

Storage is bounded too: the event store may hold at most `STORAGE_MAX / 5` bytes
of events, and once that is reached the **oldest live** events are evicted. An
evicted event is gone from every node that received it.

### Querying

`GET /events` filters the live set. All parameters are optional.

| Parameter | Behaviour |
| --- | --- |
| `collection` | Exact match. |
| `label` | Exact match against any one label. |
| `owner` | Exact match; case-insensitive on input. |
| `search` | Case-insensitive substring match against the canonical `data` text. |
| `blob` | Exact match; must be 64 hex characters. |
| `since` / `until` | Inclusive bounds on `created_at`. |
| `limit` | Page size, 1–100. Default 50. |
| `cursor` | Opaque keyset position, see below. |

Results are ordered `created_at` descending, with the event `id` as a
tiebreaker — the same ordering that makes pagination stable.

#### Pagination

`next_cursor` is `"<created_at>:<id>"` and means **older than this position**.
Pass it back unchanged:

```bash
curl "http://localhost:3232/events?collection=notes&limit=50" | jq -r '.next_cursor'
curl "http://localhost:3232/events?collection=notes&limit=50&cursor=1761012000:6191ca75…"
```

`next_cursor` is empty on the last page, so a client never has to make a
trailing request just to discover the end. A page that exactly fills `limit` is
already the last page and returns an empty cursor.

The response envelope carries the list twice, `events` and `records`, with the
same contents; `records` exists for older clients. `cursor` echoes what you sent.

### Live streams

`GET /events/stream` is Server-Sent Events. Any browser reaches it with
`EventSource`; a terminal with `curl -N`. No WebSocket, no library.

```bash
curl -N "http://localhost:3232/events/stream?collection=notes"
```

Accepted live filters: `collection`, `label`, `owner`, `search`, `blob`,
`since`, `until`. `limit` is accepted and ignored. `cursor` is **rejected** with
`400`, because its "events older than here" semantics would suppress everything
new. Resume with `Last-Event-ID` instead.

#### Frames

| Frame | When |
| --- | --- |
| `retry: 3000` | First frame. Tells `EventSource` to reconnect after 3 seconds. |
| `: connected` | First frame. A comment; carries no event. |
| `: keepalive` | Every 15 seconds, so the connection stays observable. |
| `event: event` + `id:` + `data:` | One delivered event, JSON in `data`, the event `id` in `id`. |

```text
retry: 3000
: connected

event: event
id: 6191ca75010530180a865daa5c1271dbf4172b487c55c5b966e73aa1d25475a9
data: {"id":"6191ca75…","owner":"ed25519:79b5…","collection":"notes","data":{…}}
```

#### Resuming

Because every event frame carries `id`, a browser reconnects with
`Last-Event-ID: <last id>` and the server replays everything published after
that anchor, oldest first. A client that was briefly offline catches up instead
of silently losing events.

Replay is capped at the **newest 100** events, so a client returning after a long
gap lands on current state rather than a flood. If the anchor is unknown — the
event expired, was evicted, or the node never held it — the stream simply starts
live, no error. The subscriber is attached *before* the backlog is read, so
nothing published during that window can slip through the gap.

#### Bounds

At most **256** concurrent subscribers per node; past that, `503`. A slow
consumer cannot pin a handler forever: each frame is written under a write
deadline, a failed write ends that stream, and a subscriber whose 64-slot queue
overflows loses the oldest pending frame rather than stalling the publisher.

### Federation protocol

Nodes converge on the **union** of each other's event sets by pulling over the
same public HTTP API. There is no new protocol and no broker.

This is the property that makes Originless a replacement for a relay rather than
a different relay: relays do not replicate to each other, so a client holds N
independent sockets and is responsible for writing to every one of them. Here the
fan-out is the nodes' job. See [what this replaces](#what-this-replaces).

```bash
docker run -d --name o1 -p 127.0.0.1:3233:3232 -e SYNC_NODES=http://localhost:3234 ghcr.io/besoeasy/originless:latest
docker run -d --name o2 -p 127.0.0.1:3234:3232 -e SYNC_NODES=http://localhost:3233 ghcr.io/besoeasy/originless:latest
```

Every 5 seconds each node, per peer:

1. reads its checkpoint from the `peer_cursors` table (survives restart),
2. `GET <peer>/events?limit=100&since=<checkpoint − 900s>`,
3. rebuilds each event without its server-assigned fields,
4. runs it through **the same validation as a publish** — signature, TTL,
   drift — so a peer cannot inject forged events,
5. dedupes on `id`, imports, and advances the checkpoint to the newest
   `created_at` it saw.

Details worth knowing:

- **Checkpoints are on `created_at`, with a 15-minute lookback.** That window
  matches the server's future-drift bound exactly, so an event a peer accepted
  with a slightly-ahead clock still lands on the next round. Dedupe makes the
  redundancy free.
- **Skipping is deliberate.** An event that fails validation is logged and
  skipped permanently; the checkpoint moves past it. A *persistence* failure is
  not: the round aborts so the checkpoint never advances past an event that was
  not stored.
- **Events only.** IPFS blobs stay on the node that accepted them. A federated
  event can reference a `blob` or a CID the receiving node cannot resolve.
- **Clock skew matters.** Backdating an event beyond the 15-minute window is not
  guaranteed to sync. Keep `created_at` at "now".

A round reads at most 50 pages; the next round continues from wherever it got
to, so a peer with a large backlog drains over several rounds instead of
stalling the loop.

## HTTP API reference

Base URL: `http://localhost:3232` (configurable with `PORT`). Everything is
plain HTTP JSON on one port; the embedded Kubo node's API is internal and never
exposed.

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/` | HTML dashboard and live demos |
| `GET` | `/healthz` | App and IPFS node liveness |
| `GET` | `/stats` | IPFS repository and event metrics |
| `POST` | `/up` | Upload a file or folder |
| `POST` | `/upf` | Upload a folder |
| `GET` | `/cid/{cid}` | Metadata and availability for a CID |
| `POST` | `/events` | Publish a signed event |
| `GET` | `/events` | Query events |
| `GET` | `/events/{id}` | Retrieve one event |
| `GET` | `/events/stream` | Live event stream (SSE) |

### Conventions

**CORS is wide open.** Every response carries
`Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: GET, POST, OPTIONS, HEAD`
and `Access-Control-Allow-Headers: *`; `OPTIONS` answers `204`. There are no
cookies, tokens or sessions for a browser to withhold, so there is nothing to
protect. See [access control](#access-control) before exposing
the port to an untrusted network.

**Errors** are JSON with a single `error` string:

```json
{"error": "limit must be between 1 and 100"}
```

**Status codes** used across the API:

| Code | Meaning |
| --- | --- |
| `400` | Malformed request, invalid filter, failed validation |
| `401` | Signature did not verify against `owner` |
| `404` | No such event or CID, or the event has expired |
| `405` | Wrong method; `Allow` header lists the accepted ones |
| `413` | Body exceeds the size limit for the route |
| `415` | `POST /events` with a non-`application/json` content type |
| `502` | The IPFS node rejected an add |
| `503` | IPFS node unreachable, or too many stream subscribers |
| `507` | Not enough free space to accept the upload |

**Limits**

| Limit | Value |
| --- | --- |
| Event body | 8 KiB |
| Event TTL | 1 year |
| Event `created_at` drift | 15 minutes into the future |
| Labels per event | 10, unique |
| Event page size | 50 default, 100 maximum |
| Stream subscribers | 256 |
| Upload body | 1 GiB of file data (+ 1 MiB multipart overhead) |
| Stored events | `STORAGE_MAX / 5` |

### `GET /`

The dashboard: node status, live chat, shared canvas and upload dropzone.
Embedded in the binary, so it works with no IPFS node running. Served with
`Cache-Control: no-cache`.

### `GET /healthz`

Liveness of both the app and the IPFS node. A container whose node is
unreachable cannot serve uploads, content or stats, so reporting it healthy
would be misleading — this answers `503` instead. It is also the container
`HEALTHCHECK` target.

```json
{"status": "ok", "ipfs": "ok"}
```

```json
{"status": "degraded", "ipfs": "unavailable"}
```

### `GET /stats`

Kubo repository statistics merged with event-store metrics. Answers `503` when
the IPFS node is unreachable.

```json
{
  "NumObjects": 12,
  "SizeStat": {"RepoSize": 2048, "StorageMax": 20000000000},
  "events": {
    "count": 4,
    "total": 5,
    "expired": 1,
    "unique_owners": 2,
    "top_collections": [{"collection": "notes", "count": 3}],
    "top_labels": [{"label": "notes:lobby", "count": 2}],
    "stored_bytes": 2048,
    "oldest_created_at": 1758420000,
    "newest_created_at": 1758420100,
    "subscribers": 1,
    "max_subscribers": 256,
    "db_bytes": 40960
  },
  "events_evicted": 0
}
```

| Field | Meaning |
| --- | --- |
| `NumObjects`, `SizeStat` | Kubo's repository statistics. `RepoSize` is bytes used, `StorageMax` the `STORAGE_MAX` limit. |
| `events.count` | Live, unexpired events. |
| `events.total` | Events resident in the store, **including** expired ones the hourly reaper has not reclaimed yet. |
| `events.expired` | Expired but not yet reaped. Never served by any read. |
| `events.unique_owners` | Distinct signer public keys among live events. |
| `events.stored_bytes` | Sum of event payload sizes. |
| `events.oldest_created_at`, `events.newest_created_at` | Live-event time span. Omitted when there are none. |
| `events.subscribers` / `max_subscribers` | Open SSE streams against the 256 cap. |
| `events.db_bytes` | On-disk size of `events.db` including its WAL and SHM files. |
| `top_collections`, `top_labels` | Ten most-used values among live events, count then name. |
| `events_evicted` | Events dropped because the event byte budget was exceeded. TTL expiry is **not** counted here. |

### `POST /up`

`multipart/form-data` with one or more file parts. A single plain file is added
as a file; multiple parts, a filename containing `/`, or a directory part is
treated as a folder and wrapped with a root directory.

```bash
curl -F "file=@hello.txt" http://localhost:3232/up
```

Relative paths are preserved in the multipart `filename`, and an
`X-File-Path` header overrides it. Paths are cleaned and rejected if they are
absolute or escape the upload root, and two parts with the same path are a
`400`. Parts with `Content-Type: application/x-directory` become empty
directories.

Body parts are spooled to disk before being sent to IPFS, so the free space on
the spool filesystem is checked first: `507` with `Retry-After: 60` when a 1 GiB
upload cannot plausibly fit.

```json
{
  "cid": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN",
  "size": 14,
  "bytes": 18,
  "name": "hello.txt",
  "extension": ".txt",
  "mime": "application/octet-stream",
  "files": 1
}
```

| Field | Meaning |
| --- | --- |
| `cid` | CID of the uploaded file or folder root. |
| `size` | Size IPFS reports for the returned root. Falls back to `bytes` when the node reports none. |
| `bytes` | Total bytes of uploaded file data. |
| `name` | Root name. Omitted when the node reports none. |
| `extension` | Lowercase extension of the submitted filename; empty for folders. |
| `mime` | Derived from the extension; see below. |
| `files` | Number of file parts. Omitted for a single-file add. |

`mime` comes from the node's MIME database, with `inode/directory` for folders
and `application/octet-stream` for anything unknown or extensionless. The
container image ships no `/etc/mime.types`, so only `.jpg`, `.jpeg`, `.opus` and
`.bin` are guaranteed to resolve to something specific; `.txt`, for instance,
reports `application/octet-stream` there while a full distro build would say
`text/plain`.

### `POST /upf`

Identical to `/up` but always uses folder mode, wrapping the parts in a root
directory even for a single file.

```bash
curl \
  -F "file=@folder/one.txt;filename=folder/one.txt" \
  -F "file=@folder/nested/two.txt;filename=folder/nested/two.txt" \
  http://localhost:3232/upf
```

Uploads are added with `pin=false`, so nothing is pinned and Kubo's hourly GC at
the 90% watermark may reclaim them. Mount a volume at `/data` and treat a CID as
a reference, not a guarantee.

### `GET /cid/{cid}`

Everything the **local** node reports about a CID, plus an `available` flag that
is true only when the block is present locally.

```bash
curl http://localhost:3232/cid/QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN
```

```json
{
  "cid": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN",
  "available": true,
  "block": {"Key": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN", "Size": 14},
  "object": {
    "Hash": "QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN",
    "NumLinks": 0,
    "BlockSize": 0,
    "LinksSize": 0,
    "DataSize": 6,
    "CumulativeSize": 14,
    "Type": "file"
  },
  "data": "aGVsbG8K"
}
```

| Field | Present when |
| --- | --- |
| `block` | The local block exists. |
| `object` | The node can describe the UnixFS root. |
| `object.Type` | `"file"` or `"directory"` from the node's `files/stat`. Absent on nodes that only speak the older `object/stat`. |
| `object.Blocks` | Number of blocks the file is stored in, when reported. |
| `links` | The root is a directory. |
| `data` | Single-block content no larger than 1 MiB, base64-encoded. |
| `json` | That content parsed as JSON. |
| `error` | The block is missing locally, with `available: false` and no `block`/`object`. |

A missing block answers `200` with `available: false` — the query worked, the
content is simply not here. `503` means the IPFS node itself is unreachable.

#### How the node is queried

All three lookups — block, UnixFS stat, directory listing — are sent with
`offline=true`, so they describe **this** repository and nothing else. Without
it, Kubo would try to fetch an absent block from the public network, turning a
local availability check into a multi-second wait for a misleading answer.

The UnixFS stat is read from `files/stat`, which replaced the `object/stat`
endpoint removed from current Kubo; `object/stat` is still tried as a fallback so
older nodes keep working. `Type` therefore identifies a directory on a current
node, where `NumLinks` is absent.

### `POST /events`

Publish a signed event. `Content-Type: application/json` is required if the
header is sent at all; the body must be ≤ 8 KiB. The format and signing recipe
are specified in [the event protocol](#the-event-protocol).

```bash
curl -X POST http://localhost:3232/events \
  -H "Content-Type: application/json" \
  -d '{"owner":"ed25519:…","collection":"notes","created_at":1758420000,
       "expires_at":1761012000,"data":{"note":"hello"},"labels":[],
       "sig":"…"}'
```

```json
{"status": "success", "id": "6191ca75…", "stored_at": "2026-09-20T09:20:00Z"}
```

| Status | When |
| --- | --- |
| `201 Created` | Stored for the first time. |
| `200 OK` | Already known — `{"duplicate": true, "id": …, "stored_at": …}`. |
| `400` | Malformed JSON, unknown or missing field, bad collection/label, TTL out of range, or already expired. |
| `401` | `bad sig` — the signature does not verify against `owner`. |
| `413` | Body over 8 KiB. |
| `415` | Content type is not `application/json`. |
| `500` | The event could not be persisted; retry is safe. |

### `GET /events`

Query live events with `collection`, `label`, `owner`, `search`, `blob`,
`since`, `until`, `limit` and `cursor`. Newest first; defaults and matching
rules are in [querying](#querying).

```bash
curl "http://localhost:3232/events?collection=notes&label=notes:lobby&limit=20"
```

```json
{
  "status": "success",
  "events": [{"id": "6191ca75…", "collection": "notes", "data": {"note": "hello"}}],
  "records": [{"id": "6191ca75…", "collection": "notes", "data": {"note": "hello"}}],
  "limit": 20,
  "cursor": "",
  "next_cursor": "1758420000:6191ca75…"
}
```

`events` and `records` are the same list; `records` is kept for older clients.
`next_cursor` is empty on the last page.

### `GET /events/{id}`

```json
{"status": "success", "event": {"id": "6191ca75…", "…": "…"}, "record": {"id": "6191ca75…", "…": "…"}}
```

An unknown, evicted or expired id answers `404` — expired events are
indistinguishable from absent ones, by design.

### `GET /events/stream`

Server-Sent Events. Frames, filters, resume behaviour and bounds are in
[live streams](#live-streams).

```bash
curl -N "http://localhost:3232/events/stream?collection=notes"
```

Responses are `text/event-stream; charset=utf-8` with `Cache-Control: no-cache, no-transform`
and `X-Accel-Buffering: no`, so reverse proxies do not buffer it. A comment
keepalive is sent every 15 seconds. `503` when the 256-subscriber cap is
reached; `400` for an invalid filter or for `cursor`.

## Configuration and operations

Every setting has a working default, so the container runs with no
configuration at all.

### Environment variables

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

### What is stored where

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

### Sizing storage

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

### Access control

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

### Federation operations

Set `SYNC_NODES` on each node to the other's base URL and they converge on the
union of both event logs over plain HTTP, every 5 seconds. This is what removes
the single point of failure a one-node deployment would otherwise have — compare
a relay, which loses its share of the log when it goes away.

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

The protocol is described in full under [federation protocol](#federation-protocol).

### Observability

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

### Behind a reverse proxy

Set a long or disabled read timeout for `/events/stream`: the connection is
meant to stay open, and idle periods are filled by a 15-second keepalive
comment. Originless already sends `Cache-Control: no-cache, no-transform` and
`X-Accel-Buffering: no`, but a proxy that buffers anyway will stall delivery.

Serve over **HTTPS** in front of a proxy. Browsers only expose
`crypto.subtle` in secure contexts; the bundled dashboard carries a pure-JS
Ed25519 signer as a fallback so the demos still work over plain HTTP, but your
own clients should not have to.

### Backup and restore

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

### Troubleshooting

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

## Architecture

For contributors: how the code is laid out, what guarantees the event store
makes, and which invariants are load-bearing.

- [Packages](#packages)
- [Startup](#startup)
- [The event store](#the-event-store)
- [Publish path](#publish-path)
- [Live stream path](#live-stream-path)
- [Federation path](#federation-path)
- [IPFS boundary](#ipfs-boundary)
- [Dashboard](#dashboard-implementation)
- [Tests](#tests)
- [Build and release](#build-and-release)
- [Load-bearing invariants](#load-bearing-invariants)

### Packages

```text
cmd/originless         main: PORT, config, IPFS client, router, signal handling
internal/config        environment parsing; derives the event byte budget
internal/events        event schema, verification, store, reaper, filters, SSE, HTTP handler
internal/sync          peer pull loop over the public /events endpoints
internal/ipfs          minimal Kubo HTTP API client
internal/server        router, CORS, embedded dashboard, stats/upload/cid/health handlers
internal/testutil      test helpers: signs events with the same recipe as a client
scripts/example-event.go   reproducible signed event used by the worked example here
```

The dependency direction is one-way: `server` and `sync` both call into
`events`; nothing calls back out. `events` knows nothing about HTTP routing
beyond its own handler, which is why the same store can be driven by a client,
a test, or a federation round.

### Startup

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

### The event store

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

#### Concurrency

A single `sync.Mutex` guards the map, the subscriber set and the byte counters.
The lock is held only for bookkeeping: the fan-out to subscribers happens after
it is released, and each event is deep-copied (`cloneEvent`) on the way out so
callers can never mutate stored state or observe a torn read.

Subscriber channels are buffered (64) and written **non-blockingly**: a consumer
that cannot keep up loses the pending frame and is logged, instead of stalling
the publisher. Slow readers degrade themselves rather than the node.

#### Expiry

Expiry is enforced at read time and never by a read. Expired events stay
resident — invisible to every query, still counted in `total` — until the
reaper's hourly pass deletes them. The invariant: *a read never mutates the
retained set*, so a reap is the only thing that can shrink it and the observable
behaviour of the API does not depend on when the reaper happens to run.

### Publish path

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

### Live stream path

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

### Federation path

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

### IPFS boundary

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
- Every CID lookup is sent with `offline=true`. Without it Kubo fetches an
  absent block from the public network, so a local-only availability check
  stalls and then reports something misleading.
- The UnixFS stat is read from `files/stat` with `object/stat` kept as a
  fallback, because Kubo removed the latter. The two shapes differ — only the
  old one reports `NumLinks` — so `ObjectStat.IsDirectory` and `ContentSize`
  normalize the difference and the handler never has to know which node answered.

The spool-then-add design in `internal/server/upload.go` is what makes the 1 GiB
upload limit safe: parts land on disk first, free space is checked before any
body is read, paths are cleaned against traversal, and temp files are removed on
every exit path including failures.

### Dashboard implementation

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

### Tests

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
| `TestCIDLookupsAreOffline` | A CID is described from the local repository only |
| `TestCIDInfoAgainstModernKubo` | `files/stat` answers the UnixFS stat current nodes serve |
| `TestCIDInfoListsLinksForModernDirectory` | Directories are recognised without `NumLinks` |
| `TestUploadRejectsPathTraversal` | Multipart paths are contained |
| `TestHealthzReportsUnavailableNode` | Health never lies about a dead node |

### Build and release

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

### Load-bearing invariants

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

## Two things to know

**There is no access control, by design.** No accounts, no passwords, no API
tokens: an Ed25519 signature proves who wrote an event, not that they were
allowed to. CORS is `*` because there is nothing to protect. Bind to
`127.0.0.1` to keep a run private, and put authorization in your own client or a
reverse proxy when you need it. Full reasoning in
[access control](#access-control).

**Uploads are ephemeral.** Nothing is pinned, so Kubo's garbage collector may
reclaim a CID. Keep your own copy of anything you need to survive that.

## Development

Requires Go 1.24+, and a `kubo` install for the full single-process experience
on `localhost:5001`.

```bash
go vet ./...      # lint
go build ./...    # build
go test ./...     # test
./podman.sh       # build the image and run it locally, foreground, --rm
```

The app reads `PORT`, `STORAGE_MAX` and `SYNC_NODES`; when running outside the
container it also expects `/data` to be writable, or point
`config.EventsDBPath` elsewhere in code. See [architecture](#architecture).

## Links

- [Kubo](https://github.com/ipfs/kubo)
- [IPFS HTTP Gateway specification](https://specs.ipfs.tech/http-gateways/)
- [inbrowser.link](https://inbrowser.link/ipfs/) to open a CID in a browser

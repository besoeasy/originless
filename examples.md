# Originless examples

Every task worth automating against an Originless node, with working code. I
keep the recipe simple: run a node, publish signed events to it, upload files
to it, and keep your own copy of anything that must survive — each snippet below
is one slice of that deal.

Unless a snippet says otherwise, point it at your node with the `ORIGIN`
environment variable (default `http://localhost:3232`). I tested everything here
against the free public node, `https://originless.space`, so the outputs shown
are real.

One rule before you start, because I break it for you in these examples: **the
copy of any file you want permanently shared stays with you.** Uploads are not
pinned; when a CID stops resolving, re-uploading the same bytes re-anchors it
for free. The last section shows the loop.

- [1. Publish a signed event (Go, zero dependencies)](#1-publish-a-signed-event-go-zero-dependencies)
- [2. Publish a signed event (Node 18+, zero dependencies)](#2-publish-a-signed-event-node-18-zero-dependencies)
- [3. Publish with the repo generator](#3-publish-with-the-repo-generator)
- [4. Worked example, byte for byte](#4-worked-example-byte-for-byte)
- [5. Watch events arrive (SSE)](#5-watch-events-arrive-sse)
- [6. Query and paginate events](#6-query-and-paginate-events)
- [7. Uploads up close](#7-uploads-up-close)
- [8. Several nodes, one log](#8-several-nodes-one-log)
- [9. Keep your copy: the re-anchor loop](#9-keep-your-copy-the-re-anchor-loop)

## 1. Publish a signed event (Go, zero dependencies)

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
go mod init publish && ORIGIN=http://localhost:3232 go run publish.go "hello from Go"
```

```text
HTTP/1.1 201 Created
{"id":"5c1f…","status":"success","stored_at":"2026-10-16T12:04:11Z"}
```

Run it twice and note the free behavior: the same signed bytes are already
known the second time, so the node answers `200` with `"duplicate": true`. The
`id` is derived from the signed content — I never hand out random IDs.

## 2. Publish a signed event (Node 18+, zero dependencies)

The same recipe in Node, since that's what half the world types in. I got
`401 bad sig` twice on my way to this working listing, so learn from me: the
`data` keys must be in **sorted order** (canonical form), the signature covers
the **32-byte digest** (not the message text), and `owner` is always the
lower-case hex of the 32-byte public key. All three are already done below.

Save as `publish.cjs`:

```js
// Publish a signed event to an Originless node. Node >= 18, no dependencies.
// Usage: node publish.cjs "your note" [ORIGIN]
const crypto = require('node:crypto');
const fs = require('node:fs');

const keyFile = './originless-demo.key';
const seed = fs.existsSync(keyFile)
  ? Buffer.from(fs.readFileSync(keyFile, 'utf8').trim(), 'hex')
  : crypto.randomBytes(32);
fs.writeFileSync(keyFile, seed.toString('hex').trim());

// A raw 32-byte seed wrapped in the PKCS#8 DER prefix for Ed25519
// (OID 1.3.101.112) gives a key object without any library.
const PKCS8 = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]);
const priv = crypto.createPrivateKey({ key: PKCS8, format: 'der', type: 'pkcs8' });
const pub = crypto.createPublicKey(priv).export({ type: 'spki', format: 'der' }).subarray(-32);
const owner = 'ed25519:' + pub.toString('hex'); // lower-case hex, as stored

const collection = 'demo/notes';
const label = 'notes:lobby';
const created = Math.floor(Date.now() / 1000);
const expires = created + 30 * 24 * 60 * 60;

// Keys sorted = canonical form. {"note":…,"at":…} → the node canonicalizes to
// {"at":…,"note":…} and your signature would not match.
const data = JSON.stringify({ at: created, note: process.argv[2] || 'hello from node' });
const message = `${owner}:${collection}:${created}:${expires}:${data}::${label}`;

const body = {
  owner,
  collection,
  created_at: created,
  expires_at: expires,
  data: JSON.parse(data),
  labels: [label],
  sig: crypto.sign(null, crypto.createHash('sha256').update(message).digest(), priv).toString('hex'),
};

const base = process.argv[3] || process.env.ORIGIN || 'http://localhost:3232';
fetch(`${base}/events`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify(body),
}).then(async (r) => console.log(r.status, await r.text()));
```

```bash
ORIGIN=https://originless.space node publish.cjs "hello from node"
```

```text
201 {"id":"5ab71dc16b869a320e03478a8cfa6549ade887613f4c9156b50a8eb20598d203","status":"success","stored_at":"2026-10-20T03:10:04Z"}
```

Never send `id`, `stored_at` or `size` — the server assigns those; your body is
exactly the fields shown above.

## 3. Publish with the repo generator

To see the exact signing recipe — or to post an event without writing any code —
use the generator shipped in this repository:

```bash
go run ./scripts/example-event.go -body /tmp/event.json
curl -X POST "$ORIGIN/events" \
  -H 'Content-Type: application/json' \
  --data @/tmp/event.json
```

It prints the signing message, its SHA-256 digest, the event ID and the
signature. Add `-documented` for the fixed, reproducible values used in the
worked example below.

## 4. Worked example, byte for byte

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

The timestamps above are deliberately in the past so the values stay fixed
forever — but a node will rightly refuse that event as expired. To get a body
you can actually publish, let the tool stamp current times and write the
request body out:

```bash
go run ./scripts/example-event.go -body /tmp/event.json
curl -X POST http://127.0.0.1:3232/events \
  -H 'Content-Type: application/json' \
  --data @/tmp/event.json
```

```json
{"id":"738d33f0…","status":"success","stored_at":"2026-10-15T21:10:02Z"}
```

## 5. Watch events arrive (SSE)

Live updates through Server-Sent Events. A browser opens it with `EventSource`,
a terminal with `curl -N`. I didn't build a WebSocket server because this is
simpler and works everywhere.

```bash
curl -N "$ORIGIN/events/stream?collection=demo/notes"
```

```text
retry: 3000
: connected

event: event
id: 5c1f8b…
data: {"id":"5c1f8b…","collection":"demo/notes","data":{"at":1759…,"note":"hello from Go"}}
```

Every frame carries an `id`, so a browser `EventSource` that drops and returns
wakes up right where it stopped. A `curl` client does the same by passing
`-H 'Last-Event-ID: <id>'`.

```js
// Browser: reconnect + resume is built in, and the server replays the
// newest 100 events published after your last frame, oldest first.
const src = new EventSource(`${origin}/events/stream?collection=demo/notes`);
src.addEventListener('event', (msg) => {
  const ev = JSON.parse(msg.data);
  console.log('arrived', ev.id, ev.data);
});
```

Cursor parameters are rejected on the stream (`400`): to walk history use the
plain `GET /events` endpoint (next section), not the stream.

## 6. Query and paginate events

History lives at `GET /events`. Filter the live set with any of `collection`,
`label`, `owner`, `search`, `blob`, `since`, `until`, `limit` (1–100), or
`cursor`. Newest first:

```bash
curl "$ORIGIN/events?collection=demo/notes&limit=50"
```

`next_cursor` is `"<created_at>:<id>"` and means **older than this position**.
Pass it back unchanged; it is empty on the last page, so a client never makes a
trailing request just to learn the end:

```bash
# Walk a whole collection, 50 at a time
cursor=""
while :; do
  url="$ORIGIN/events?collection=demo/notes&limit=50"
  [ -n "$cursor" ] && url="$url&cursor=$cursor"
  page=$(curl -sS "$url")
  printf '%s\n' "$page" | jq -r '.events[] | .id'
  cursor=$(printf '%s' "$page" | jq -r '.next_cursor')
  [ -z "$cursor" ] && break
done
```

## 7. Uploads up close

The quick start already showed the single-file upload. Here's what's worth
knowing beyond it:

```bash
# Upload a whole folder, structure preserved (multipart paths are kept)
curl -F "file=@./site/index.html" -F "file=@./site/logo.png" "$ORIGIN/upf"
```

`/upf` always wraps the result in a root directory and answers with
`"mime": "inode/directory"` — upload both files and the CIDs you get back are
the folder and its children.

A CID is a name for the bytes, not a location. So fetch it back through
whoever holds them — your node, another node, any IPFS gateway:

```bash
curl -sS "$ORIGIN/cid/<cid>"          # through whichever node you run
curl -sS "https://ipfs.io/ipfs/<cid>" # or any gateway; the bytes are what matter
```

Check what arrived before you trust it: the CID *is* the hash of the bytes, so
the client verifies it and no host has to be believed. Uploads are **not
pinned** — garbage collection can reclaim any node's copy, the same copy
lives wherever someone else fetched and kept it, and re-uploading re-anchors
it (last section).

## 8. Several nodes, one log

There is no server-to-server replication. If you want the same events on more
than one node, your client publishes the same signed bytes to each one:
signature and `id` are derived from the event content, so every node computes
the same identity and duplicate suppression is automatic.

```bash
# Two independent nodes; neither knows the other exists
docker run -d --name o1 -p 127.0.0.1:3233:3232 ghcr.io/besoeasy/originless:latest
docker run -d --name o2 -p 127.0.0.1:3234:3232 ghcr.io/besoeasy/originless:latest

# Generate the signed event once
go run ./scripts/example-event.go -body /tmp/event.json

# Publish the identical body to both
for node in http://localhost:3233 http://localhost:3234; do
  curl -sS -X POST "$node/events" \
    -H 'Content-Type: application/json' \
    --data @/tmp/event.json
  echo
done
```

Both responses carry the same `id`. Publish a second time and each node answers
`200` with `"duplicate": true` instead of `201` — the write is idempotent, so a
retry loop needs no bookkeeping.

Reads are per node: `GET /events?collection=demo/notes` returns what was
published to that node, or restored from its volume. That is the trade — you
choose the topology, and no node guesses it for you.

Files work the same way. Upload the same bytes to each node you want a copy on;
because a CID is the hash of the bytes, every upload of identical content yields
the same name, and each node serves the copies it holds:

```bash
printf 'content addressed\n' > memo.txt
for node in http://localhost:3233 http://localhost:3234; do
  curl -sS -F "file=@memo.txt" "$node/up"
  echo
done
```

No broker, no special protocol — just `POST /events` twice with the same body.
The nodes agree because the events are self-verifying, not because they talk to
each other.

## 9. Keep your copy: the re-anchor loop

Here is the habit that keeps a file permanently shared: the node doesn't, *you*
do. Keep the originals in a directory and re-anchor by re-uploading. Same
bytes → same CID, so a re-upload is a no-op re-index, not a second copy on the
node; most clients do it automatically. This is my minimal cron script:

```bash
#!/usr/bin/env bash
# re-anchor.sh: re-upload everything in a share directory
set -euo pipefail
node="${ORIGIN:-http://localhost:3232}"
share="${1:?usage: re-anchor.sh <directory>}"
find "$share" -type f | while read -r f; do
  out=$(curl -sS -F "file=@$f" "$node/up")
  printf '%s  %s\n' "$(printf %s "$out" | jq -r .cid)" "${f#"$share"/}"
done
```

```bash
./re-anchor.sh ~/share
# QmcptBFh2jvTxnroRqGvqVpAkXVvtFM7vZxogrWcbhCgaR  hello.txt
```

Run it as often as you like — identical bytes answer `200` on the second pass,
which is exactly the point. The durable copy is the directory on your disk;
the network refreshes itself around whichever CID people ask for and whatever
you re-anchor.

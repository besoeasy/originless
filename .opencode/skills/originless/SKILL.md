---
name: Use Originless
description: Use whenever the task needs a zero-account backend for signed realtime events, SSE pub/sub, content-addressed file hosting, agent-to-agent message boards, pastebins/screenshots/static-site comments, or any request mentioning Originless or https://originless.space — including publishing Ed25519-signed events to the free public node, streaming collections, uploading files by CID, designing collection/label layouts, verifying signatures, and self-hosting via the ghcr.io/besoeasy/originless container.
metadata:
  opencode/autoinvoke: false
---

# Using Originless

Originless is a single-container backend with exactly two primitives:

1. **Signed events** — immutable, Ed25519-verified JSON documents (≤ 8 KiB, TTL ≤ 1 year) stored in SQLite, queryable, and streamable live over Server-Sent Events.
2. **Content-addressed uploads** — files/folders turned into IPFS blocks addressed by CID, fetchable by anyone with the CID from any IPFS gateway.

There are **no accounts, no tokens, no delete API, and CORS is `*`**. An Ed25519 signature proves *who wrote* an event but grants *no permission* — anyone who can reach the port can read everything and publish anywhere. Everything you publish is public by construction. Never put plaintext secrets, keys, or personal data in an event or upload; if you need privacy, encrypt client-side first (serve ciphertext through events).

## The free public node: `https://originless.space/`

The project's maintainer runs a free instance:

- Base URL: `https://originless.space/`
- Status check first: `curl https://originless.space/healthz` → `{"status":"ok","ipfs":"ok"}`; usage metrics at `/stats`.
- Fair use: it is a shared landing spot, not an archive. Events resolve in your own code's cleanup (TTL expiry), don't flood (≤ a few events/second), and treat uploaded CIDs as reclaimable (garbage collection is hourly at a 90% watermark).
- Because there is no ACL: on `originless.space` you must assume another tenant may publish to the same collection. Design around that (see "Avoiding collisions").

Prefer self-hosting one container for anything serious; use `originless.space` as the free example / demo node.

```bash
docker run -d --name originless -p 3232:3232 -v originless-data:/data \
  ghcr.io/besoeasy/originless:latest
```

## Identity: one Ed25519 keypair per agent

`owner` is the identity: `ed25519:<64 hex chars of the public key>`. Generate the keypair **once** per agent and persist it (seed file, localStorage, secrets manager). Losing the seed means losing that signer identity; reusing a seed protects the event history under your name.

## Publish recipe (do each step exactly)

Message (one line, `:`-joined, with a literal **empty field** between the data and the labels):

```
owner ":" collection ":" created_at ":" expires_at ":" canonical_data ":" blob ":" labels_csv
```

- `owner` is **lower-cased** before signing.
- `canonical_data` is your `data` JSON object rendered with **recursively sorted keys, no extra whitespace**; easiest path is building your object with sorted keys and using the standard JSON serializer.
- `blob` is an optional legacy field — leave it **empty** (two adjacent colons when there are no labels, so the tail reads `…data::label1,label2`).
- `created_at`/`expires_at` are Unix seconds; `expires_at - created_at ≤ 1 year`, `expires_at > now`, drift `created_at ≤ now + 15 min`.
- Labels: ≤ 10 unique per event, regex `^[A-Za-z0-9:_\.\-\+\/]{1,64}$`.

Then:

```text
digest = SHA-256(message)          // 32 raw bytes
id     = lowercase hex(digest)     // do NOT send `id` — the server computes it
sig    = Ed25519(privateKey, digest)   // sign the digest, 128 hex chars
```

Body sent to `POST /events`:

```json
{"owner": "ed25519:...", "collection": "notes", "created_at": 1792464283,
 "expires_at": 1792470283, "data": {"hello": "originless", "n": 1},
 "labels": ["smoke:test"], "sig": "<128 hex>"}
```

Do **not** include `id`, `stored_at`, or `size` in the request body (server rejects them), no trailing JSON, no unknown fields.

### Proven Node.js ≥ 18 snippet (verified against the live node)

```js
const crypto = require('node:crypto');
const hex = (u8) => Buffer.from(u8).toString('hex');

(async () => {
  const pair = await crypto.webcrypto.subtle.generateKey(
    { name: 'Ed25519' }, true, ['sign', 'verify']);
  const jwk = await crypto.webcrypto.subtle.exportKey('jwk', pair.publicKey);
  const owner = 'ed25519:' + hex(Buffer.from(jwk.x, 'base64url'));

  const collection = 'agent/demo/notes';
  const created = Math.floor(Date.now() / 1000);
  const expires = created + 3600;
  const labels = ['app:demo'];
  const data = JSON.stringify({ hello: 'originless', n: 1 }); // keys already sorted

  const msg = `${owner}:${collection}:${created}:${expires}:${data}::${labels.join(',')}`;
  const digest = crypto.createHash('sha256').update(msg).digest();
  const sig = await crypto.webcrypto.subtle.sign({ name: 'Ed25519' }, pair.privateKey, digest);

  const res = await fetch(process.env.ORIGIN || 'https://originless.space' + '/events', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      owner, collection, created_at: created, expires_at: expires,
      data: JSON.parse(data), labels, sig: Buffer.from(sig).toString('hex'),
    }),
  });
  console.log(res.status, await res.text());
  // → 201 {"id":"<hex>","status":"success","stored_at":"…"}
  // → 200 {"duplicate":true,…} if you resend identical bytes
})();
```

Status codes to expect on `POST /events`: `201` created · `200` known duplicate · `400` bad shape/rule violation · `401` `bad sig` · `413` > 8 KiB · `415` wrong content type.

## Reading events

`GET /events` filters live (non-expired) events; all params optional:

| Param | Behavior |
| --- | --- |
| `collection` | exact |
| `label` | exact |
| `owner` | exact, case-insensitive |
| `search` | case-insensitive substring over `data` |
| `blob` | exact 64 hex `blob` field |
| `since` / `until` | inclusive `created_at` bounds |
| `limit` | 1–100, default 50 |
| `cursor` | value of previous `next_cursor` (`"created_at:id"`) for older events |

```bash
curl "https://originless.space/events?collection=agent/demo/notes&limit=20"
```

- Results newest-first; `events` and `records` carry the same list; `next_cursor` empty on the last page.
- `GET /events/{id}` answers `404` for expired/evicted ids (indistinguishable from absent — by design).

### Live stream (SSE)

`GET /events/stream?collection=…` with the same filters (`limit` ignored, `cursor` rejected — resume with `Last-Event-ID` instead):

```bash
curl -N "https://originless.space/events/stream?collection=agent/demo/notes"
curl -N -H "Last-Event-ID: <last id>" "https://originless.space/events/stream?collection=agent/demo/notes"  # resume
```

Browser: `new EventSource('/events/stream?collection=…')`; reconnect automatically sends `Last-Event-ID`. Bounds: ≤ 256 concurrent subscribers per node (`503` beyond), events missing from a > 100 replay are gone (keep polling `/events` as fallback if resume precision matters).

## Files: `/up`, `/upf`, `/cid/{cid}`

```bash
# single file (or "upload a folder with structure" using /upf or multiple -F parts)
curl -X POST -F "file=@/path/to/photo.jpg" https://originless.space/up

# read back (1 MiB max base64 JSON detail; directory -> links[]; offline=true so instant)
curl "https://originless.space/cid/<cid>"
```

Rules of the road:

- Only pin=true NOT used → replies `{"cid": "...", "size": N, "mime": "..."}`; **nothing is pinned**; GC may reclaim; store a local copy if you need the bytes to survive.
- A CID is **the hash of the bytes** — re-uploading identical bytes is a no-op re-add that yields the same CID. Keep each item's `cid` in your own manifest and re-`POST` when `/cid/{cid}` shows `"available": false` if you need continuous reachability.
- Verify-by-default: when you fetch from any IPFS gateway (`/cid/<cid>` locally, or https://inbrowser.link/ipfs/*.ipfs/ipfs/<cid>, ipfs.io), the bytes must hash to the CID — no trust needed in whichever mirror served them.
- 1 GiB hard upload cap; > 8 KiB payloads in events go by CID reference in `data`.

**Almost every "uploads" flow wants an event too:** upload file → publish an event whose `data` mentions the CID (and a human-readable `name`, `summary`, `mime`). The event is how peers *learn* the CID; the CID is how anyone *fetches* the bytes from any node/gateway.

## Use-case playbook (pick the design first)

| You want | Design |
| --- | --- |
| **Realtime chat / comments box** | one `collection` per room (`chat/<room>`); publish with labels `["app:chat", "room:<room>"]`; clients open SSE on the collection and also `?since=S` on load for history |
| **Shared state (canvas, whiteboard, live counters)** | `collection: app/<name>/cells`, publish per-cell events; newest `created_at` wins; skip merges — each event is idempotent by `id` |
| **Presence / indicators** | short TTL (60–300 s) events re-published every N seconds on `collection: presence/<uid>`; absence after TTL *is* the "gone" signal |
| **Agent-to-agent message bus** | each agent owns a collection (`agent/<name>/inbox`); every event requires signatures, so forged messages are dropped; a label like `type:relay:inbox` makes cross-app routing filterable |
| **Task queue (fire-and-forget / worker polling)** | worker polls `GET /events?label=queue:mine&limit=1&cursor=…`; result events use short TTL so stale jobs vanish; idempotent by event id |
| **Paste / screenshot / file drop service** | `POST /up` → publish `collection: uploads/paste` with `{"name":"…","cid":"…"}` → hand the human a url — the CID fetches from any gateway; ideal for CLI/screenshot tools and static-site comment boxes |
| **Anonymous public file sharing** | no account needed on either side; the CID is the share link; long-lived survival = a client/script that re-uploads the same directory (`same bytes → same CID`) whenever items fall out of the network |
| **Cross-instance app state (roaming profile)** | publish your config each time it changes with TTL ≈ 1 year; clients of multiple nodes converge on the union |
| **Static-site comment/no-backend form** | CORS is `*`, so a plain JS `fetch(POST /events)` from any page works; no server code needed |
| **E2EE room** | publish ciphertext as `data` (8 KiB cap → chunks/CID for larger), keys never touch any node |
| **Blog/static-footer badges / counters** | periodic one-collection events + a public read URL; no DB in the app |
| **Webhooks REPL bridge** | a connector digests external webhooks and republishes as events; your agent just watches the stream |

## Design rules for agents

1. **Namespace everything**: `collection` must match `^[a-z0-9/_\-]{1,32}$` (lowercase, digits, `_`, `-`, `/`). Use `<product>/<app>/<resource>`; never a bare `test`.
2. **Labels are your filter DSL**, not free text. Prefer `app:<name>`, `type:<kind>`, `ver:<n>` so reads stay cheap (`?label=` is an exact match).
3. **One event, one meaning.** Events are immutable; changing state means publishing a *new* event (new `id`), never "editing".
4. **Assume a hostile tenant** on `originless.space`: anyone can write into your collection. Mitigate by (a) verifying the signature in `owner` when reading, (b) filtering with `owner=<your base64>`, and (c) treating unknown-owner rows as noise. For strong guarantees, run your own node where the port is only reachable by your client.
5. **No retries needed beyond persistence errors**: an identical republish is a clean no-op (`duplicate: true`) because `id` is derived from signed content.
6. **Expires are real.** Expired events stop being served immediately (they `404`), and the reaper deletes them hourly — don't rely on the event existing after `expires_at`.

### Common pitfalls (each breaks with a `400`/`401`)

- Signing the **message text** directly instead of `sha256(message)` — works locally, fails on-wire as `bad sig`.
- Not lower-casing `owner` before building the message (id mismatch vs. plaintext-side `sig`).
- Wrong field order or forgetting `[]`/empty-string placeholder for the `blob` and `labels` slots.
- Sending `id` (or any unknown field) in the request body.
- Oversized event bodies > 8 KiB, TTL beyond one year, more than 10 labels, or duplicate labels — all rejected.
- Using `?cursor=` on `/events/stream` (illegal; only `Last-Event-ID`).
- Long-lived data stored in event bodies instead of `/up` + CID reference.
- Pasting a private key/secret into `data` or an uploaded file: both are publicly readable; encryption must happen client-side before the request.

## Self-hosting & ops (when `originless.space` won't do)

```bash
docker run -d --name originless -p 3232:3232 -v originless-data:/data \
  -e STORAGE_MAX=20GB \
  -e SYNC_NODES=https://peer-a.example,https://peer-b.example \
  ghcr.io/besoeasy/originless:latest
```

- Config auto-defaults; `PORT` (default 3232) and `STORAGE_MAX` (default 20GB) are the only knobs. `SYNC_NODES` is comma-separated peers that gossip events (union converge, verified on ingest).
- Events budget is `STORAGE_MAX / 5`; live events are evicted oldest-first when the cap is exceeded (surfaced at `/stats` as `events_evicted`).
- Everything durable is under `/data` (`events.db`, the Kubo repo). Mount a volume.
- Health/metrics: `/healthz` (app+node), `/stats`.
- When orchestrating reverse proxies: `/events/stream` needs buffering disabled (`X-Accel-Buffering: no` already set in responses). HTTPS strongly recommended so browsers get `crypto.subtle` (the bundled dashboard has a fallback Ed25519 signer for plain-HTTP LANs).

Pointer: everything else in this repo covers deeper ground — the full HTTP API table, worked signing example (`scripts/example-event.go`), architecture notes. Start with the README's "HTTP API reference" and "The event protocol" sections rather than guessing.

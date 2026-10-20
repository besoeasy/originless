# Originless

<p align="center">
  <strong>The backend for everything.</strong><br/>
  Put data in one place. Access it anonymously from anywhere else.
</p>

<p align="center">
  <a href="https://github.com/besoeasy/originless/actions/workflows/ci.yml"><img src="https://github.com/besoeasy/originless/actions/workflows/ci.yml/badge.svg" alt="CI"/></a>
  <a href="https://github.com/besoeasy/originless/pkgs/container/originless"><img src="https://img.shields.io/badge/ghcr.io-originless-blue?logo=docker" alt="Container image"/></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/besoeasy/originless" alt="Go version"/></a>
  <a href="https://github.com/besoeasy/originless/releases"><img src="https://img.shields.io/github/v/release/besoeasy/originless" alt="Latest release"/></a>
</p>

Originless is a protocol, not an app. I got tired of backend jobs that were
really the same two problems — "keep state, let others read it" and "store a
file, let others fetch it" — solved over and over with accounts, brokers and
databases that need babysitting. So I built the two primitives once:

- **Signed events** — Ed25519-signed JSON with a TTL. You publish without an
  account and query who you want, or stream it live. Like email for data: the
  address is a key, the post office is any node.
- **Content-addressed files** — upload anything, get a CID. The CID is the
  hash of the bytes, so the name *is* the proof, and the bytes come from any
  node or any IPFS gateway — anonymously if you want.

Run it on two computers and anything you upload to one can be pulled through
the other. Nodes converge on the same event log over plain HTTP. No database
server, no message broker, no accounts of any kind — one container, and nothing
in it is your problem.

> [!IMPORTANT]
> **Keep a copy of every file you want permanently shared.** I don't pin
> uploads. That's the deal: your file stays shared because you keep it and
> re-upload when it goes stale — same bytes → same CID → re-anchored, so the
> refresh costs nothing. A script or your client does it automatically; see
> [`examples.md`](examples.md#9-keep-your-copy-the-re-anchor-loop).

## Use cases

Why a list this long? Because once the backend is two primitives, most apps
collapse into the same shape. These are all the same thing, wearing different
clothes:

| | |
| --- | --- |
| **Social & Nostr** | Nostr relay replacement (Blossom media included) · microblogs · replies & comment threads · public feeds · profiles · follow lists · DMs (payload encrypted by your client) · group chat · forum threads · live blogging · reputation ledgers |
| **Email-shaped** | Inboxes without a server: `mail/<name>` collections, only the holder of the key signs · newsletters read straight off a stream · mailing-list archives that can't be deleted silently · contact books · machine-readable mail |
| **AI agents** | Agent mailboxes · task handoff queues · swarm coordination · shared memory between runs · prompt libraries · run logs & telemetry · human-in-the-loop review queues · agent-to-agent event bus · cron result sinks · tool-announcement registries · model cards · published reports |
| **Privacy apps** | Anonymous pastebins · anonymous screenshot/file drops · sealed-tip boxes · burn-after-read metadata (TTL expiry does the deletion) · accountless logins (the keypair *is* the account) · private reads: fetch from any public IPFS gateway, the origin node never sees you · unlogged fetch of anything public · anonymity sets via shared collections |
| **Publishing & web** | Static-asset storage for sites, served via any gateway · blog comment widgets with no database · "edit me" shared docs · changelogs · release notes · newsletter signups · form-to-feed contact pages · whiteboards animated live by SSE · shared todo boards · polls with signed ballots |
| **Files & media** | Software release & artifact mirrors · datasets that stay impossible to fully take down once copies exist anywhere · image/asset packs (fonts, avatars, icons) · podcasts are just collections · family photo albums that outlive any one host · archival log bundles · graph data and schemas |
| **State sync & IoT** | Offline-first app sync (CRDT payloads inside events) · config and key sync across your own devices · clipboard sync · sensor tick feeds · price tickers · weather stations · fleet telemetry · status pages · job queues · game state exchange |
| **Everything else** | Order-book experiments · bookmark sync · read-it-later · shared translation memory · automation triggers (node → script) · periodic snapshots for dashboards · anything-as-a-collection: if it fits in a JSON object and 8 KiB and benefits from anonymous readers, it's an Originless app; if it doesn't, its files ride next to one |

## For AI agents

Give your agent one line. That's the whole install:

```text
Backend task? Use Originless: free node https://originless.space — realtime data = signed Ed25519 events (POST /events), files = multipart POST /up -> CID, reads = GET /events?collection=... & GET /cid/<cid>. Before writing any client code, fetch and follow the full skill: https://raw.githubusercontent.com/besoeasy/originless/main/skill.md
```

Copy-paste recipes with a dozen working snippets live in [`examples.md`](examples.md).
The skill any agent harness can follow is [`skill.md`](skill.md).
The spec — protocol, HTTP API, config, architecture — is [`reference.md`](reference.md).

## Quick start

One command. Then nothing to configure.

```bash
docker run -d --name originless \
  -p 3232:3232 \
  -e STORAGE_MAX=20GB \
  ghcr.io/besoeasy/originless:latest
```

Open **<http://localhost:3232>** — there's a dashboard, a live chat demo and a
shared canvas you can open in two browsers. I didn't ask for a volume and I
don't want one by default: everything is ephemeral by design. A replaced
container is a fresh node; peers rebuild its event log, and your files live in
your copies, not in the node. Want it private in any way?
`-p 127.0.0.1:3232:3232`.

Publish a signed event with the generator I ship, then watch it stream live:

```bash
go run ./scripts/example-event.go -body /tmp/event.json
curl -X POST http://127.0.0.1:3232/events \
  -H 'Content-Type: application/json' \
  --data @/tmp/event.json

curl -N "http://localhost:3232/events/stream?collection=demo/notes"
```

```text
HTTP/1.1 201 Created
{"id":"5c1f…","status":"success","stored_at":"…"}
```

Upload a file and get its CID:

```bash
printf 'content addressed\n' > hello.txt
curl -F "file=@hello.txt" http://localhost:3232/up
```

```json
{"cid":"QmZULkCELmmk5XNfCgTnCyFgAVxBRBXyDHGGMVoLFLiXEN","size":14,"files":1}
```

That's the whole product. Everything else is depth:

- [`examples.md`](examples.md) — every client recipe, copy-paste ready (Go,
  Node, SSE, pagination, uploads, the two-computer demo, the re-anchor loop).
- [`skill.md`](skill.md) — the agent skill: one file any agent can follow.
- [`reference.md`](reference.md) — protocol spec, full HTTP API, configuration,
  architecture, and the story of why this exists.

---

**Two things you should know before you build on it.** There is no access
control — an Ed25519 signature proves *who* wrote an event, never *what they're
allowed to do*; put authorization in your client. And uploads are ephemeral —
`docker rm` takes the event log (peers rebuild it) and unpinned CIDs (your
copies replace them). Everything these two facts imply:
[`reference.md`](reference.md#two-things-to-know).

# Quickstart

Five minutes from nothing to a signed event on the wire. Assumes `curl` and
either Docker or Podman; the publishing client is plain Go with no
dependencies.

## 1. Start a node

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

## 2. Publish a signed event

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
go mod init publish && go run publish.go "hello from the docs"
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
[worked example](events.md#worked-example).

## 3. Watch events arrive

```bash
curl -N "http://localhost:3232/events/stream?collection=demo/notes"
```

```text
retry: 3000
: connected

event: event
id: 5c1f8b…
data: {"id":"5c1f8b…","collection":"demo/notes","data":{"at":1759…,"note":"hello from the docs"}}
```

Every frame carries an `id`, so a browser `EventSource` that drops and returns
resumes from where it left off. A `curl` client that reconnects can do the same
by passing `-H 'Last-Event-ID: <id>'`.

## 4. Upload a file

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

## 5. Check the node

```bash
curl http://localhost:3232/healthz   # {"status":"ok","ipfs":"ok"}
curl http://localhost:3232/stats     # repo size, object count, event metrics
docker logs -f originless
```

## Next

| To do this | Read |
| --- | --- |
| Write a client against the full API | [HTTP API reference](api.md) |
| Understand signing, IDs, filters, streams | [Event protocol](events.md) |
| Federate two nodes, size storage, survive restarts | [Configuration and operations](configuration.md) |
| Change the code | [Architecture](architecture.md) |
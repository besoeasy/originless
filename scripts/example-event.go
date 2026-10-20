// Command example-event prints a fully reproducible, signed Originless event.
//
// The keypair comes from a fixed seed, so pinning created_at reproduces the
// worked example in examples.md exactly:
//
//	go run ./scripts/example-event.go -now 1758420000
//
// Omit -now and the timestamps are current, which is what you want before
// posting to a live node (an event whose expires_at has passed is rejected).
// Use -body to write the request body to a file:
//
//	go run ./scripts/example-event.go -body /tmp/event.json
//	curl -X POST http://localhost:3232/events \
//	  -H 'Content-Type: application/json' \
//	  --data @/tmp/event.json
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"time"
)

const (
	seedHex    = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	collection = "notes"
	label      = "notes:lobby"
	ttlSecs    = 30 * 24 * 60 * 60
	document   = "1758420000" // created_at used by the worked example in examples.md
	documentTo = "1761012000" // expires_at used by the worked example in examples.md
)

var (
	at     = flag.Int64("now", 0, "created_at to use; 0 means now")
	body   = flag.String("body", "", "also write the request body to this path")
	asDocs = flag.Bool("documented", false, "shorthand for -now 1758420000")
)

func main() {
	flag.Parse()

	created := *at
	if *asDocs {
		created, _ = strconv.ParseInt(document, 10, 64)
	}
	if created == 0 {
		created = time.Now().Truncate(time.Second).Unix()
	}
	expires := created + ttlSecs
	if *asDocs {
		// Pin both timestamps so the documented digest and signature are stable
		// no matter what the default TTL is.
		expires, _ = strconv.ParseInt(documentTo, 10, 64)
	}

	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		log.Fatalf("decode seed: %v", err)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	owner := "ed25519:" + hex.EncodeToString(public)

	// Object keys are sorted before signing, so the order they are written in
	// here does not matter: canonicalJSON sorts them again.
	raw, err := json.Marshal(map[string]any{
		"author": "ada",
		"note":   "keys are the identity",
		"rank":   7,
	})
	if err != nil {
		log.Fatalf("marshal data: %v", err)
	}
	canonical := canonicalJSON(raw)

	message := owner + ":" + collection + ":" +
		strconv.FormatInt(created, 10) + ":" + strconv.FormatInt(expires, 10) + ":" +
		string(canonical) + "::" + label
	digest := sha256.Sum256([]byte(message))
	signature := ed25519.Sign(private, digest[:])
	if !ed25519.Verify(public, digest[:], signature) {
		log.Fatal("signature does not verify against its own owner")
	}

	payload := map[string]any{
		"owner":      owner,
		"collection": collection,
		"created_at": created,
		"expires_at": expires,
		"data":       json.RawMessage(canonical),
		"labels":     []string{label},
		"sig":        hex.EncodeToString(signature),
	}
	bodyJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Fatalf("marshal event: %v", err)
	}

	fmt.Printf("owner:       %s\n", owner)
	fmt.Printf("collection:  %s\n", collection)
	fmt.Printf("created_at:  %d\n", created)
	fmt.Printf("expires_at:  %d\n", expires)
	fmt.Printf("data:        %s\n", canonical)
	fmt.Printf("labels:      [%s]\n", label)
	fmt.Printf("message:     %s\n", message)
	fmt.Printf("digest:      %s\n", hex.EncodeToString(digest[:]))
	fmt.Printf("id:          %s\n", hex.EncodeToString(digest[:]))
	fmt.Printf("sig:         %s\n", hex.EncodeToString(signature))

	if *body != "" {
		if err := os.WriteFile(*body, append(bodyJSON, '\n'), 0o644); err != nil {
			log.Fatalf("write body: %v", err)
		}
		fmt.Printf("\nrequest body written to %s (%d bytes)\n", *body, len(bodyJSON))
		return
	}
	fmt.Printf("\nrequest body (%d bytes):\n%s\n", len(bodyJSON), bodyJSON)
}

// canonicalJSON returns compact JSON with recursively sorted object keys and
// numbers preserved exactly as written — the form the server re-derives before
// it verifies a signature.
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

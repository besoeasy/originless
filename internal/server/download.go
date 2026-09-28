package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/besoeasy/originless/internal/ipfs"
)

type downloadHandler struct {
	client *ipfs.Client
}

const ipfsPathPrefix = "/ipfs/"

// downloadWriteTimeout is how long a single write to the client may stall
// before the transfer is abandoned. The deadline is refreshed on every chunk,
// so a slow but progressing client is never cut off mid-download, while a
// client that stops reading releases its goroutine and its IPFS connection.
const downloadWriteTimeout = 30 * time.Second

func (h *downloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, ipfsPathPrefix) {
		http.NotFound(w, r)
		return
	}
	prefix := ipfsPathPrefix
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed",
		})
		return
	}

	rawCID := strings.TrimPrefix(r.URL.Path, prefix)
	cid, err := url.PathUnescape(rawCID)
	if err != nil || cid == "" || strings.ContainsAny(cid, "/?#") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "CID not found"})
		return
	}

	resp, err := h.client.Cat(r.Context(), cid)
	if err != nil {
		log.Printf("IPFS download failed for %s: %v", cid, err)
		status := http.StatusBadGateway
		var ipfsErr *ipfs.StatusError
		if errors.As(err, &ipfsErr) && ipfsErr.StatusCode() == http.StatusNotFound {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": "content unavailable"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", cid))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": cid}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Bound how long the client may stall. SetWriteDeadline arms a deadline on
	// the underlying connection, so each Write must refresh it for long
	// transfers to survive.
	controller := http.NewResponseController(w)
	setWriteDeadline := func() error {
		return controller.SetWriteDeadline(time.Now().Add(downloadWriteTimeout))
	}
	if err := setWriteDeadline(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		log.Printf("set download deadline: %v", err)
	}

	w.WriteHeader(http.StatusOK)
	// Small buffer: enough to keep the pipe busy without holding large
	// amounts of an upload-sized body in memory per connection.
	buffer := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				log.Printf("stream IPFS download %s: %v", cid, writeErr)
				return
			}
			if err := setWriteDeadline(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				log.Printf("stream IPFS download %s: %v", cid, readErr)
			}
			return
		}
	}
}

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/besoeasy/originless/internal/ipfs"
)

type cidHandler struct {
	client *ipfs.Client
}

// isMissingBlockError reports whether a Kubo block/stat failure means the
// content simply is not on this node. Kubo reports missing blocks with a 500
// whose message carries the cause, so the message has to be inspected.
func isMissingBlockError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "key does not exist") || strings.Contains(msg, "blockservice: key not found")
}

const maxCIDContentBytes = 1 << 20

type cidResponse struct {
	CID       string           `json:"cid"`
	Available bool             `json:"available"`
	Block     *ipfs.BlockStat  `json:"block,omitempty"`
	Object    *ipfs.ObjectStat `json:"object,omitempty"`
	Links     []ipfs.LsLink    `json:"links,omitempty"`
	Data      string           `json:"data,omitempty"`
	JSON      json.RawMessage  `json:"json,omitempty"`
	Error     string           `json:"error,omitempty"`
}

func (h *cidHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed",
		})
		return
	}

	cid := r.PathValue("cid")
	if cid == "" || strings.ContainsAny(cid, "/?#") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "CID not found"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	response := cidResponse{CID: cid}

	block, err := h.client.BlockStat(ctx, cid)
	if err != nil {
		var apiErr *ipfs.StatusError
		if errors.As(err, &apiErr) && (apiErr.StatusCode() == http.StatusNotFound || isMissingBlockError(err)) {
			response.Available = false
			response.Error = "content unavailable on this node"
			log.Printf("IPFS block stat reported unavailable for %s: %v", cid, err)
			writeJSON(w, http.StatusOK, response)
			return
		}
		log.Printf("IPFS block stat failed for %s: %v", cid, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "IPFS node unavailable",
		})
		return
	}
	response.Available = true
	response.Block = &block

	obj, err := h.client.ObjectStat(ctx, cid)
	if err != nil {
		log.Printf("IPFS object stat failed for %s: %v", cid, err)
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.Object = &obj

	if obj.NumLinks > 0 {
		links, err := h.client.Ls(ctx, cid)
		if err != nil {
			log.Printf("IPFS ls failed for %s: %v", cid, err)
		} else {
			response.Links = links
		}
	} else if obj.CumulativeSize <= maxCIDContentBytes {
		data, decoded, err := h.client.Content(ctx, cid)
		if err != nil {
			log.Printf("IPFS cat failed for %s: %v", cid, err)
		} else {
			response.Data = base64.StdEncoding.EncodeToString(data)
			response.JSON = decoded
		}
	}

	writeJSON(w, http.StatusOK, response)
}

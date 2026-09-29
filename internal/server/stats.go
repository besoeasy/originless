package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/besoeasy/originless/internal/events"
	"github.com/besoeasy/originless/internal/ipfs"
)

type statsResponse struct {
	ipfs.IPFSStats
	Events events.EventStats `json:"events"`
	// Evicted counts events dropped because MAX_EVENTS was reached.
	Evicted int `json:"events_evicted"`
}

type statsHandler struct {
	client *ipfs.Client
	events *events.Store
}

func (h *statsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/stats" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	stats, err := h.client.RepoStats(ctx)
	if err != nil {
		log.Printf("IPFS stats request failed: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "IPFS node unavailable",
		})
		return
	}
	eventStats := events.EventStats{}
	evicted := 0
	if h.events != nil {
		eventStats = h.events.Stats(time.Now())
		evicted = h.events.Evicted()
	}
	writeJSON(w, http.StatusOK, statsResponse{
		IPFSStats: stats,
		Events:    eventStats,
		Evicted:   evicted,
	})
}

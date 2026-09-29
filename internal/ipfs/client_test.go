package ipfs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepoStats(t *testing.T) {
	var method string
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"NumObjects": 12,
			"RepoPath":   "/data/ipfs",
			"SizeStat": map[string]uint64{
				"RepoSize":   2048,
				"StorageMax": 4096,
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	stats, err := client.RepoStats(context.Background())
	if err != nil {
		t.Fatalf("RepoStats() error = %v", err)
	}

	if method != http.MethodPost {
		t.Errorf("request method = %q, want %q", method, http.MethodPost)
	}
	if requestPath != "/api/v0/stats/repo" {
		t.Errorf("request path = %q, want %q", requestPath, "/api/v0/stats/repo")
	}
	if stats.NumObjects != 12 || stats.SizeStat.RepoSize != 2048 || stats.SizeStat.StorageMax != 4096 {
		t.Errorf("stats = %+v, want expected repository statistics", stats)
	}
}

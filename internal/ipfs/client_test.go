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

// Kubo returns RepoSize and StorageMax at the top level of stats/repo.
func TestRepoStatsKuboTopLevelShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"RepoSize":8028,"StorageMax":20000000000,"NumObjects":1,"RepoPath":"/data/ipfs","Version":"fs-repo@18"}`))
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
	if stats.NumObjects != 1 || stats.SizeStat.RepoSize != 8028 || stats.SizeStat.StorageMax != 20000000000 {
		t.Errorf("stats = %+v, want RepoSize=8028 StorageMax=20000000000 NumObjects=1", stats)
	}
}

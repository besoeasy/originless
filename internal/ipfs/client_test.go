package ipfs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Every CID lookup must stay local: without offline=true Kubo attempts a
// network fetch for content this node does not have, which stalls the request.
func TestCIDLookupsAreOffline(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v0/block/stat":
			_, _ = w.Write([]byte(`{"Key":"Qm1","Size":74}`))
		case "/api/v0/files/stat":
			_, _ = w.Write([]byte(`{"Hash":"Qm1","Size":6,"CumulativeSize":14,"Blocks":1,"Type":"file"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.BlockStat(context.Background(), "Qm1"); err != nil {
		t.Fatalf("BlockStat() error = %v", err)
	}
	if _, err := client.ObjectStat(context.Background(), "Qm1"); err != nil {
		t.Fatalf("ObjectStat() error = %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %v, want block/stat and files/stat", queries)
	}
	for _, query := range queries {
		if !strings.Contains(query, "offline=true") {
			t.Errorf("query %q is not offline", query)
		}
	}
}

// Current Kubo serves files/stat and removed object/stat, so files/stat is the
// primary source and its shape maps onto the shared stat.
func TestObjectStatUsesFilesStat(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v0/files/stat" {
			http.Error(w, `{"Message":"removed, use 'ipfs dag' or 'ipfs files' instead"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"Hash":"Qm1","Size":6,"CumulativeSize":14,"Blocks":1,"Type":"file"}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	stat, err := client.ObjectStat(context.Background(), "Qm1")
	if err != nil {
		t.Fatalf("ObjectStat() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/api/v0/files/stat" {
		t.Errorf("paths = %v, want only files/stat to be called", paths)
	}
	if stat.Type != "file" || stat.DataSize != 6 || stat.CumulativeSize != 14 || stat.Blocks != 1 {
		t.Errorf("stat = %+v, want files/stat values", stat)
	}
	if stat.IsDirectory() {
		t.Errorf("IsDirectory() = true, want false for a file")
	}
	if stat.ContentSize() != 14 {
		t.Errorf("ContentSize() = %d, want 14", stat.ContentSize())
	}
}

// Older nodes serve only object/stat, so it has to remain a working fallback.
func TestObjectStatFallsBackToObjectStat(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v0/object/stat" {
			_, _ = w.Write([]byte(`{"Hash":"Qm1","NumLinks":2,"BlockSize":74,"DataSize":72,"CumulativeSize":74}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	stat, err := client.ObjectStat(context.Background(), "Qm1")
	if err != nil {
		t.Fatalf("ObjectStat() error = %v", err)
	}
	if len(paths) != 2 || paths[0] != "/api/v0/files/stat" || paths[1] != "/api/v0/object/stat" {
		t.Errorf("paths = %v, want files/stat then object/stat", paths)
	}
	if stat.NumLinks != 2 || !stat.IsDirectory() {
		t.Errorf("stat = %+v, want a directory with two links", stat)
	}
}

// When neither endpoint can describe the CID, the modern failure is the one
// worth reporting.
func TestObjectStatReportsFilesStatError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"Message":"block was not found locally (offline)"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.ObjectStat(context.Background(), "Qm1"); err == nil {
		t.Fatal("ObjectStat() error = nil, want an error when both endpoints fail")
	}
}

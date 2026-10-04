package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/besoeasy/originless/internal/config"
	"github.com/besoeasy/originless/internal/ipfs"
	"github.com/besoeasy/originless/internal/testutil"
)

func TestHomePageIsEmbeddedAndDoesNotRequireIPFS(t *testing.T) {
	client, err := ipfs.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	router := mustNewRouter(t, client)
	for _, target := range []string{"/", "/index.html"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("target %s status = %d, want %d", target, recorder.Code, http.StatusOK)
		}
		if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("target %s content type = %q, want text/html", target, contentType)
		}
		body := recorder.Body.String()
		for _, want := range []string{"Originless", `fetch("/stats"`, "Refresh stats", "Shared Canvas", "Signed Chat"} {
			if !strings.Contains(body, want) {
				t.Errorf("target %s response body does not contain %q", target, want)
			}
		}
	}
}

func TestStatsJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"NumObjects":7,"RepoPath":"/repo","SizeStat":{"RepoSize":1024,"StorageMax":0},"Version":"fs-repo@16"}`))
	}))
	defer server.Close()

	client, err := ipfs.NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/stats", nil)
	router := mustNewRouter(t, client)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("content type = %q, want application/json", contentType)
	}
	var response statsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.NumObjects != 7 || response.SizeStat.RepoSize != 1024 {
		t.Errorf("stats = %+v, want expected statistics", response)
	}
	if response.Events.Total != 0 || response.Events.Count != 0 || response.Events.UniqueOwners != 0 {
		t.Errorf("event stats = %+v, want empty event stats", response.Events)
	}
}

func TestStatsIncludesEventStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"NumObjects":1,"RepoPath":"/repo","SizeStat":{"RepoSize":10,"StorageMax":100},"Version":"fs-repo@18"}`))
	}))
	defer server.Close()

	client, err := ipfs.NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	router := mustNewRouter(t, client)
	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix(), now.Unix()+3600, "chat", map[string]any{"message": "hello"}, []string{"room:lobby"}, "")
	testutil.PublishTestEvent(t, router, raw)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response statsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Events.Count != 1 || response.Events.Total != 1 || response.Events.UniqueOwners != 1 {
		t.Errorf("event counts = %+v, want one active event and owner", response.Events)
	}
	if len(response.Events.TopCollections) != 1 || response.Events.TopCollections[0].Collection != "chat" || response.Events.TopCollections[0].Count != 1 {
		t.Errorf("top collections = %+v, want chat:1", response.Events.TopCollections)
	}
}

func TestStatsJSONUnavailable(t *testing.T) {
	client, err := ipfs.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/stats", nil)
	mustNewRouter(t, client).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("content type = %q, want application/json", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "IPFS node unavailable") {
		t.Errorf("response body = %q, want unavailable message", recorder.Body.String())
	}
}

func TestHealthzJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	mustNewRouter(t, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("content type = %q, want application/json", contentType)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "ok" {
		t.Errorf("payload = %+v, want status ok", payload)
	}
}

// A container whose IPFS node is unreachable cannot serve uploads, downloads
// or stats, so healthz must not report it healthy.
func TestHealthzReportsUnavailableNode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("node down"))
	}))
	defer upstream.Close()

	client, err := ipfs.NewClient(upstream.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "degraded" || payload["ipfs"] != "unavailable" {
		t.Errorf("payload = %+v, want degraded/unavailable", payload)
	}
}

func TestHealthzReportsHealthyNode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"NumObjects":1,"SizeStat":{"RepoSize":10,"StorageMax":0},"Version":"fs-repo@18"}`))
	}))
	defer upstream.Close()

	client, err := ipfs.NewClient(upstream.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "ok" || payload["ipfs"] != "ok" {
		t.Errorf("payload = %+v, want ok/ok", payload)
	}
}

func TestCORSMiddleware(t *testing.T) {
	preflight := httptest.NewRecorder()
	mustNewRouter(t, nil).ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, "/events", nil))

	if preflight.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", preflight.Code, http.StatusNoContent)
	}

	get := httptest.NewRecorder()
	mustNewRouter(t, nil).ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	for _, recorder := range []*httptest.ResponseRecorder{preflight, get} {
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
		}
		if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS, HEAD" {
			t.Errorf("Access-Control-Allow-Methods = %q, want GET, POST, OPTIONS, HEAD", got)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "*" {
			t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, "*")
		}
	}
}

func TestEventsSurviveRouterRestart(t *testing.T) {
	dbPath := t.TempDir() + "/events.db"
	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix()-5, now.Unix()+3600, "chat", map[string]any{"message": "persisted"}, []string{}, "")

	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.Default()
	cfg.EventsDBPath = dbPath
	router := mustRouter(t, ctx, nil, cfg)
	resp := testutil.PublishTestEvent(t, router, raw)
	if resp.Code != http.StatusCreated {
		t.Fatalf("publish status = %d, want %d", resp.Code, http.StatusCreated)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode publish response: %v", err)
	}
	cancel()

	// A new router over the same database file simulates a process restart.
	router2 := mustRouter(t, context.Background(), nil, cfg)
	recorder := httptest.NewRecorder()
	recorder.Body.Reset()
	req := httptest.NewRequest(http.MethodGet, "/events/"+created.ID, nil)
	router2.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get after restart status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func mustNewRouter(t *testing.T, client *ipfs.Client) http.Handler {
	t.Helper()
	router, err := NewRouter(client)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

func mustRouter(t *testing.T, ctx context.Context, client *ipfs.Client, cfg config.Config) http.Handler {
	t.Helper()
	router, err := NewRouterWithOptions(ctx, client, cfg)
	if err != nil {
		t.Fatalf("NewRouterWithOptions: %v", err)
	}
	return router
}

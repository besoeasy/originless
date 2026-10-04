package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/besoeasy/originless/internal/events"
	"github.com/besoeasy/originless/internal/testutil"
)

func newPeer(t *testing.T) *events.Store {
	t.Helper()
	store, err := events.NewStoreAt(filepath.Join(t.TempDir(), "events.db"), 0)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSyncOnceImportsPeerEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	peerStore := newPeer(t)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix()-5, now.Unix()+3600, "chat", map[string]any{"message": "from-peer"}, []string{}, "")
	resp := testutil.PublishTestEvent(t, events.NewHandler(peerStore), raw)
	if resp.Code != http.StatusCreated {
		t.Fatalf("publish status = %d", resp.Code)
	}
	peer := httptest.NewServer(events.NewHandler(peerStore))
	defer peer.Close()

	localStore := newPeer(t)
	syncer := New(localStore, []string{peer.URL}, time.Second)
	syncer.SyncOnce(ctx)

	if stats := localStore.Stats(now); stats.Count != 1 {
		t.Fatalf("local count = %d, want 1 synced event", stats.Count)
	}

	// A second pass must be idempotent: dedupe by ID, no duplicates.
	syncer.SyncOnce(ctx)
	if stats := localStore.Stats(now); stats.Count != 1 {
		t.Fatalf("local count after second pass = %d, want 1", stats.Count)
	}
}

func TestSyncOnceRejectsForgedEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// A peer serving raw forged JSON gets its events rejected: the importer
	// re-verifies signatures rather than trusting the peer.
	forged := `[{"id":"deadbeef","owner":"ed25519:0000000000000000000000000000000000000000000000000000000000000000","collection":"chat","created_at":` +
		`1700000000,"expires_at":` +
		`1700003600,"data":{"message":"forged"},"labels":[],"sig":"` +
		"000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" +
		`","stored_at":"2026-01-01T00:00:00Z","size":100}]`
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","events":` + forged + `,"records":` + forged + `,"limit":100,"cursor":"","next_cursor":""}`))
	}))
	defer peer.Close()

	localStore := newPeer(t)
	New(localStore, []string{peer.URL}, time.Second).SyncOnce(ctx)
	if stats := localStore.Stats(now); stats.Count != 0 {
		t.Fatalf("local count = %d, want 0: forged events must be rejected", stats.Count)
	}
}

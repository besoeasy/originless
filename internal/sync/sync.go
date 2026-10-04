// Package sync mirrors events between Originless nodes over plain HTTP.
//
// Every node pulls from each configured peer: it fetches the peer's events
// newer than its last checkpoint, re-verifies and stores them locally, and
// expects the peer to do the same. Because dedupe is by event ID, overlap
// between rounds is harmless, and each node converges on the union of all
// peers' event sets. No new transport or protocol: just the existing
// GET /events and POST /events endpoints.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/besoeasy/originless/internal/events"
)

// DefaultInterval is how often peers are polled.
const DefaultInterval = 5 * time.Second

// overlap seconds are re-fetched on every round. It must be at least the
// server-side future-drift bound (events.MaxCreatedDrift, 15 minutes): a
// peer may accept an event whose created_at is up to that far in the
// future, and such an event must still fall inside the next round's
// refetch window or it would be permanently skipped. Dedupe by ID makes
// the redundancy cheap.
const overlap = 15 * 60

// maxPages bounds a single sync round so a misbehaving peer cannot pin the
// loop; the next round continues from the updated checkpoint.
const maxPages = 50

type Syncer struct {
	store    *events.Store
	peers    []string
	interval time.Duration
	client   *http.Client
}

func New(store *events.Store, peers []string, interval time.Duration) *Syncer {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Syncer{
		store:    store,
		peers:    append([]string{}, peers...),
		interval: interval,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Run polls every peer until ctx is done.
func (s *Syncer) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SyncOnce(ctx)
		}
	}
}

// SyncOnce makes a single pass over every peer.
func (s *Syncer) SyncOnce(ctx context.Context) {
	for _, peer := range s.peers {
		if err := s.pullFrom(ctx, peer); err != nil {
			log.Printf("sync: pulling from %s: %v", peer, err)
		}
	}
}

func (s *Syncer) pullFrom(ctx context.Context, peer string) error {
	cursor, err := s.store.SyncCursor(peer)
	if err != nil {
		return fmt.Errorf("read sync cursor: %w", err)
	}

	since := int64(0)
	if cursor > overlap {
		since = cursor - overlap
	}
	newest := cursor
	next := ""
	for page := 0; ; page++ {
		var pageNewest int64
		var checkpoint string
		_, pageNewest, checkpoint, err = s.pullPage(ctx, peer, since, next)
		if pageNewest > newest {
			newest = pageNewest
		}
		if err != nil || checkpoint == "" || page+1 >= maxPages {
			break
		}
		next = checkpoint
	}
	if newest > cursor && err == nil {
		if setErr := s.store.SetSyncCursor(peer, newest); setErr != nil {
			return fmt.Errorf("store sync cursor: %w", setErr)
		}
	}
	return err
}

// pullPage fetches one page and imports it. It reports how many events were
// imported, the newest created_at seen, and the cursor for the next page.
func (s *Syncer) pullPage(ctx context.Context, peer string, since int64, cursor string) (int64, int64, string, error) {
	endpoint, err := url.Parse(strings.TrimRight(peer, "/") + "/events")
	if err != nil {
		return 0, 0, "", err
	}
	query := endpoint.Query()
	query.Set("limit", "100")
	if since > 0 {
		query.Set("since", fmt.Sprint(since))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, 0, "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, "", fmt.Errorf("peer returned %s", resp.Status)
	}
	var body struct {
		Events     []*events.Event `json:"events"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return 0, 0, "", fmt.Errorf("decode peer response: %w", err)
	}
	imported := int64(0)
	newest := int64(0)
	for _, event := range body.Events {
		raw, err := events.SyncRecordInput(event)
		if err != nil {
			continue
		}
		created, _, err := s.store.Accept(raw, time.Now())
		if err != nil {
			log.Printf("sync: rejecting event %s from %s: %v", event.ID, peer, err)
			continue
		}
		if created {
			imported++
		}
		if event.CreatedAt > newest {
			newest = event.CreatedAt
		}
	}
	return imported, newest, body.NextCursor, nil
}

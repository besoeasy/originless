// Package events stores and serves signed, immutable JSON event documents.
package events

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	MaxEventSize               = 8192
	MaxEventTTL          int64 = 365 * 24 * 60 * 60
	MaxEventLabels             = 10
	MaxCreatedDrift      int64 = 15 * 60
	defaultEventLimit          = 50
	maxEventLimit              = 100
	maxEventSubscribers        = 256
	sseKeepaliveInterval       = 15 * time.Second
	// sseRetryInterval is advertised to EventSource clients so a dropped
	// connection is re-established quickly and can be resumed.
	sseRetryInterval = 3 * time.Second
)

// ReapInterval is how often expired events are deleted from the store.
// Expired events are never served; the reaper only reclaims their memory.
const ReapInterval = time.Hour

var (
	eventCollectionPattern = regexp.MustCompile(`^[a-z0-9/_-]{1,32}$`)
	eventLabelPattern      = regexp.MustCompile(`^[A-Za-z0-9:_\-/\.\+]{1,64}$`)
)

type Event struct {
	ID         string          `json:"id"`
	Owner      string          `json:"owner"`
	Collection string          `json:"collection"`
	CreatedAt  int64           `json:"created_at"`
	ExpiresAt  int64           `json:"expires_at"`
	Data       json.RawMessage `json:"data"`
	Blob       string          `json:"blob,omitempty"`
	Labels     []string        `json:"labels"`
	Sig        string          `json:"sig"`
	StoredAt   string          `json:"stored_at"`
	Size       int64           `json:"size"`
}

type eventInput struct {
	Owner      *string         `json:"owner"`
	Collection *string         `json:"collection"`
	CreatedAt  *int64          `json:"created_at"`
	ExpiresAt  *int64          `json:"expires_at"`
	Data       json.RawMessage `json:"data"`
	Blob       *string         `json:"blob"`
	Labels     *[]string       `json:"labels"`
	Sig        *string         `json:"sig"`
}

type eventFilter struct {
	Owner        string
	Collection   string
	Label        string
	Blob         string
	Search       string
	Since        int64
	Until        int64
	AfterCreated int64
	AfterID      string
	Limit        int
}

type eventSubscriber struct {
	ch     chan *Event
	filter eventFilter
}

type eventCollectionStat struct {
	Collection string `json:"collection"`
	Count      int    `json:"count"`
}

type eventLabelStat struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type EventStats struct {
	Count int `json:"count"`
	Total int `json:"total"`
	// Expired counts events past their TTL that the hourly reaper has not
	// reclaimed yet. Expired events are never served by any read.
	Expired         int                   `json:"expired"`
	UniqueOwners    int                   `json:"unique_owners"`
	TopCollections  []eventCollectionStat `json:"top_collections"`
	TopLabels       []eventLabelStat      `json:"top_labels"`
	StoredBytes     int64                 `json:"stored_bytes"`
	OldestCreatedAt int64                 `json:"oldest_created_at,omitempty"`
	NewestCreatedAt int64                 `json:"newest_created_at,omitempty"`
	Subscribers     int                   `json:"subscribers"`
	MaxSubscribers  int                   `json:"max_subscribers"`
}

type Store struct {
	mu          sync.Mutex
	events      map[string]*Event
	subscribers map[*eventSubscriber]struct{}
	// maxEvents bounds in-memory growth. Zero means unbounded.
	maxEvents int
	// evicted counts events dropped because the store was full.
	evicted int
	// db is the durable backing store.
	db *sql.DB
}

// DefaultDBPath is where events are persisted when nothing overrides it. The
// app always runs in a container that owns /data, so this is safe.
const DefaultDBPath = "/data/events.db"

// NewStore opens the default events database and returns a store that keeps
// at most maxEvents live events.
func NewStore(maxEvents int) (*Store, error) {
	return NewStoreAt(DefaultDBPath, maxEvents)
}

// NewStoreAt opens (creating if needed) a SQLite database at path and
// returns a store whose events survive restarts. Live, unexpired events are
// loaded into memory at open; every insert, eviction and reap is mirrored to
// the database so the two never disagree. Expired rows are purged at open.
func NewStoreAt(path string, maxEvents int) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open events database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id         TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		data       TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create events table: %w", err)
	}
	store := &Store{
		events:      make(map[string]*Event),
		subscribers: make(map[*eventSubscriber]struct{}),
		maxEvents:   maxEvents,
		db:          db,
	}
	now := time.Now()
	if _, err := db.Exec(`DELETE FROM events WHERE expires_at <= ?`, now.Unix()); err != nil {
		db.Close()
		return nil, fmt.Errorf("purge expired events: %w", err)
	}
	rows, err := db.Query(`SELECT data FROM events`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			db.Close()
			return nil, fmt.Errorf("load events: %w", err)
		}
		var event Event
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			log.Printf("events: skipping corrupt row: %v", err)
			continue
		}
		if event.ExpiresAt <= now.Unix() {
			continue
		}
		store.events[event.ID] = &event
	}
	if err := rows.Err(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load events: %w", err)
	}
	// Enforce the cap on load: keep the newest events.
	if maxEvents > 0 && len(store.events) > maxEvents {
		keep := make([]*Event, 0, len(store.events))
		for _, event := range store.events {
			keep = append(keep, event)
		}
		sort.Slice(keep, func(i, j int) bool {
			if keep[i].CreatedAt != keep[j].CreatedAt {
				return keep[i].CreatedAt > keep[j].CreatedAt
			}
			return keep[i].ID > keep[j].ID
		})
		for _, event := range keep[maxEvents:] {
			delete(store.events, event.ID)
			if err := store.dbDelete(event.ID); err != nil {
				db.Close()
				return nil, fmt.Errorf("trim events: %w", err)
			}
		}
	}
	return store, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) dbInsert(event *Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO events (id, created_at, expires_at, data) VALUES (?, ?, ?, ?)`,
		event.ID, event.CreatedAt, event.ExpiresAt, data)
	return err
}

func (s *Store) dbDelete(id string) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE id = ?`, id)
	return err
}

func (s *Store) insert(event *Event, now time.Time) (bool, string, error) {
	s.mu.Lock()
	if existing, ok := s.events[event.ID]; ok {
		storedAt := existing.StoredAt
		s.mu.Unlock()
		return false, storedAt, nil
	}

	s.evictLocked(now)
	stored := cloneEvent(event)
	stored.StoredAt = now.UTC().Format(time.RFC3339)
	if err := s.dbInsert(stored); err != nil {
		s.mu.Unlock()
		return false, "", fmt.Errorf("persist event: %w", err)
	}
	s.events[stored.ID] = stored
	subscribers := make([]*eventSubscriber, 0, len(s.subscribers))
	for subscriber := range s.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	s.mu.Unlock()

	for _, subscriber := range subscribers {
		if subscriber.filter.matches(stored) {
			select {
			case subscriber.ch <- cloneEvent(stored):
			default:
				log.Printf("events: dropping event for slow SSE subscriber")
			}
		}
	}
	return true, stored.StoredAt, nil
}

// get returns a live event by ID. Expired events are never served.
func (s *Store) get(id string, now time.Time) (*Event, bool) {
	s.mu.Lock()
	event, ok := s.events[id]
	if ok && event.ExpiresAt <= now.Unix() {
		ok = false
	}
	if ok {
		event = cloneEvent(event)
	} else {
		event = nil
	}
	s.mu.Unlock()
	return event, ok
}

// query returns matching live events, newest first, plus a cursor for the next
// page. The cursor is only returned when another page actually exists, so a
// caller never has to make a pointless trailing request.
func (s *Store) query(filter eventFilter, now time.Time) ([]*Event, string) {
	s.mu.Lock()
	events := make([]*Event, 0, len(s.events))
	for _, event := range s.events {
		if event.ExpiresAt <= now.Unix() || !filter.matches(event) {
			continue
		}
		events = append(events, cloneEvent(event))
	}
	s.mu.Unlock()

	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt != events[j].CreatedAt {
			return events[i].CreatedAt > events[j].CreatedAt
		}
		return events[i].ID > events[j].ID
	})

	nextCursor := ""
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
		last := events[len(events)-1]
		nextCursor = fmt.Sprintf("%d:%s", last.CreatedAt, last.ID)
	}
	return events, nextCursor
}

// Reap deletes expired events and reports how many were removed. Reads never
// delete: expired events stay resident until the next reap so that a reaping
// pass is the only thing that mutates the set of retained events.
func (s *Store) Reap(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.purgeExpiredLocked(now.Unix())
}

// purgeExpiredLocked removes expired events and returns how many were removed.
// Callers must hold s.mu.
func (s *Store) purgeExpiredLocked(now int64) int {
	removed := 0
	for id, event := range s.events {
		if event.ExpiresAt <= now {
			delete(s.events, id)
			if err := s.dbDelete(id); err != nil {
				log.Printf("events: persist eviction of %s: %v", id, err)
			}
			removed++
		}
	}
	return removed
}

// StartReaper deletes expired events every interval until ctx is done. A
// non-positive interval falls back to ReapInterval.
func (s *Store) StartReaper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = ReapInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if removed := s.Reap(now); removed > 0 {
					log.Printf("events: reaped %d expired event(s)", removed)
				}
			}
		}
	}()
}

// replayAfter returns matching live events published strictly after the anchor
// event, oldest first, so a reconnecting SSE client can catch up on whatever
// it missed. It reports false when the anchor is unknown, in which case the
// client has nothing to resume from.
func (s *Store) replayAfter(anchorID string, filter eventFilter, now time.Time) ([]*Event, bool) {
	s.mu.Lock()
	anchor, ok := s.events[anchorID]
	if !ok {
		s.mu.Unlock()
		return nil, false
	}
	afterCreated, afterID := anchor.CreatedAt, anchor.ID
	events := make([]*Event, 0, len(s.events))
	for _, event := range s.events {
		if event.ExpiresAt <= now.Unix() || !filter.matches(event) {
			continue
		}
		if event.CreatedAt < afterCreated || (event.CreatedAt == afterCreated && event.ID <= afterID) {
			continue
		}
		events = append(events, cloneEvent(event))
	}
	s.mu.Unlock()

	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt != events[j].CreatedAt {
			return events[i].CreatedAt < events[j].CreatedAt
		}
		return events[i].ID < events[j].ID
	})
	// Keep the newest events when a client has been away long enough to
	// overflow the cap: those are the ones that bring it up to date, and they
	// are already ordered oldest first.
	if len(events) > maxEventLimit {
		events = events[len(events)-maxEventLimit:]
	}
	return events, true
}

// evictLocked drops the oldest live events until there is room for one more.
// Callers must hold s.mu. Expired events are dropped first and for free.
func (s *Store) evictLocked(now time.Time) {
	if s.maxEvents <= 0 {
		return
	}
	if len(s.events) < s.maxEvents {
		return
	}
	removed := s.purgeExpiredLocked(now.Unix())
	for len(s.events) >= s.maxEvents {
		victim := ""
		first := true
		for id, event := range s.events {
			switch {
			case first:
				victim, first = id, false
			case event.CreatedAt < s.events[victim].CreatedAt:
				victim = id
			case event.CreatedAt == s.events[victim].CreatedAt && id < victim:
				victim = id
			}
		}
		if victim == "" {
			return
		}
		delete(s.events, victim)
		if err := s.dbDelete(victim); err != nil {
			log.Printf("events: persist eviction of %s: %v", victim, err)
		}
		removed++
	}
	s.evicted += removed
	if removed > 0 {
		log.Printf("events: store full at %d events, evicted %d oldest event(s)", s.maxEvents, removed)
	}
}

// Evicted reports how many events have been dropped because the store was full.
func (s *Store) Evicted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evicted
}

func (s *Store) subscribe(filter eventFilter) (*eventSubscriber, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxEventSubscribers > 0 && len(s.subscribers) >= maxEventSubscribers {
		return nil, false
	}
	subscriber := &eventSubscriber{ch: make(chan *Event, 64), filter: filter}
	s.subscribers[subscriber] = struct{}{}
	return subscriber, true
}

func (s *Store) unsubscribe(subscriber *eventSubscriber) {
	s.mu.Lock()
	delete(s.subscribers, subscriber)
	s.mu.Unlock()
}

func (s *Store) Stats(now time.Time) EventStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowUnix := now.Unix()
	stats := EventStats{
		Total:          len(s.events),
		TopCollections: make([]eventCollectionStat, 0),
		TopLabels:      make([]eventLabelStat, 0),
		Subscribers:    len(s.subscribers),
		MaxSubscribers: maxEventSubscribers,
	}
	owners := make(map[string]struct{})
	collections := make(map[string]int)
	labels := make(map[string]int)
	for _, event := range s.events {
		if event.Size > 0 {
			stats.StoredBytes += event.Size
		}
		if event.ExpiresAt <= nowUnix {
			stats.Expired++
			continue
		}
		stats.Count++
		owners[event.Owner] = struct{}{}
		collections[event.Collection]++
		if stats.OldestCreatedAt == 0 || event.CreatedAt < stats.OldestCreatedAt {
			stats.OldestCreatedAt = event.CreatedAt
		}
		if event.CreatedAt > stats.NewestCreatedAt {
			stats.NewestCreatedAt = event.CreatedAt
		}
		for _, label := range event.Labels {
			labels[label]++
		}
	}
	stats.UniqueOwners = len(owners)
	for collection, count := range collections {
		stats.TopCollections = append(stats.TopCollections, eventCollectionStat{Collection: collection, Count: count})
	}
	for label, count := range labels {
		stats.TopLabels = append(stats.TopLabels, eventLabelStat{Label: label, Count: count})
	}
	sort.Slice(stats.TopCollections, func(i, j int) bool {
		if stats.TopCollections[i].Count != stats.TopCollections[j].Count {
			return stats.TopCollections[i].Count > stats.TopCollections[j].Count
		}
		return stats.TopCollections[i].Collection < stats.TopCollections[j].Collection
	})
	sort.Slice(stats.TopLabels, func(i, j int) bool {
		if stats.TopLabels[i].Count != stats.TopLabels[j].Count {
			return stats.TopLabels[i].Count > stats.TopLabels[j].Count
		}
		return stats.TopLabels[i].Label < stats.TopLabels[j].Label
	})
	if len(stats.TopCollections) > 10 {
		stats.TopCollections = stats.TopCollections[:10]
	}
	if len(stats.TopLabels) > 10 {
		stats.TopLabels = stats.TopLabels[:10]
	}
	return stats
}

func cloneEvent(event *Event) *Event {
	if event == nil {
		return nil
	}
	clone := *event
	clone.Data = append(json.RawMessage(nil), event.Data...)
	clone.Labels = make([]string, len(event.Labels))
	copy(clone.Labels, event.Labels)
	return &clone
}

func (f eventFilter) matches(event *Event) bool {
	if event == nil {
		return false
	}
	if f.Owner != "" && event.Owner != f.Owner {
		return false
	}
	if f.Collection != "" && event.Collection != f.Collection {
		return false
	}
	if f.Label != "" {
		found := false
		for _, label := range event.Labels {
			if label == f.Label {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Blob != "" && event.Blob != f.Blob {
		return false
	}
	if f.Search != "" && !strings.Contains(strings.ToLower(string(event.Data)), strings.ToLower(f.Search)) {
		return false
	}
	if f.Since > 0 && event.CreatedAt < f.Since {
		return false
	}
	if f.Until > 0 && event.CreatedAt > f.Until {
		return false
	}
	if f.AfterCreated > 0 {
		if event.CreatedAt > f.AfterCreated || (event.CreatedAt == f.AfterCreated && event.ID >= f.AfterID) {
			return false
		}
	}
	return true
}

func validateEvent(raw []byte, now time.Time) (*Event, error) {
	if len(raw) > MaxEventSize {
		return nil, fmt.Errorf("event too large: %d > %d", len(raw), MaxEventSize)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("empty event body")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, exists := fields["id"]; exists {
		return nil, fmt.Errorf("id is server-computed, do not send it")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input eventInput
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("invalid event: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid trailing JSON")
	}

	if input.Owner == nil || strings.TrimSpace(*input.Owner) == "" {
		return nil, fmt.Errorf("missing owner")
	}
	if input.Collection == nil || *input.Collection == "" {
		return nil, fmt.Errorf("missing collection")
	}
	if input.CreatedAt == nil {
		return nil, fmt.Errorf("missing created_at")
	}
	if input.ExpiresAt == nil {
		return nil, fmt.Errorf("missing expires_at")
	}
	if input.Sig == nil || strings.TrimSpace(*input.Sig) == "" {
		return nil, fmt.Errorf("missing sig")
	}
	if input.Labels == nil {
		return nil, fmt.Errorf("missing labels (send [] for none)")
	}
	if len(input.Data) == 0 || bytes.Equal(bytes.TrimSpace(input.Data), []byte("null")) {
		return nil, fmt.Errorf("missing data (must be a JSON object)")
	}

	// Normalize the owner to lower case before it is signed or stored, so the
	// same key always yields the same event ID and matches an ?owner= filter.
	owner := strings.ToLower(strings.TrimSpace(*input.Owner))
	collection := *input.Collection
	createdAt := *input.CreatedAt
	expiresAt := *input.ExpiresAt
	labels := *input.Labels
	if !eventCollectionPattern.MatchString(collection) {
		return nil, fmt.Errorf("invalid collection")
	}
	if len(labels) > MaxEventLabels {
		return nil, fmt.Errorf("too many labels: maximum is %d", MaxEventLabels)
	}
	seenLabels := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if !eventLabelPattern.MatchString(label) {
			return nil, fmt.Errorf("invalid label %q", label)
		}
		if _, exists := seenLabels[label]; exists {
			return nil, fmt.Errorf("duplicate label %q", label)
		}
		seenLabels[label] = struct{}{}
	}
	if createdAt <= 0 {
		return nil, fmt.Errorf("invalid created_at")
	}
	if expiresAt <= createdAt {
		return nil, fmt.Errorf("expires_at must be greater than created_at")
	}
	if expiresAt-createdAt > MaxEventTTL {
		return nil, fmt.Errorf("TTL exceeds one year")
	}
	if createdAt > now.Unix()+MaxCreatedDrift {
		return nil, fmt.Errorf("created_at is too far in the future")
	}

	canonicalData, err := canonicalJSON(input.Data)
	if err != nil {
		return nil, fmt.Errorf("invalid data: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(canonicalData, &object); err != nil || object == nil {
		return nil, fmt.Errorf("data must be a JSON object")
	}

	blob := ""
	if input.Blob != nil {
		blob, err = normalizeEventBlob(*input.Blob)
		if err != nil {
			return nil, fmt.Errorf("invalid blob: %w", err)
		}
	}
	publicKey, err := parseEventPublicKey(owner)
	if err != nil {
		return nil, err
	}
	signature, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(*input.Sig)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid sig: must be 128 hex characters")
	}

	message := eventSigningMessage(owner, collection, createdAt, expiresAt, canonicalData, blob, labels)
	digest := sha256.Sum256(message)
	if !ed25519.Verify(publicKey, digest[:], signature) {
		return nil, fmt.Errorf("bad sig")
	}

	return &Event{
		ID:         hex.EncodeToString(digest[:]),
		Owner:      owner,
		Collection: collection,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
		Data:       canonicalData,
		Blob:       blob,
		Labels:     append([]string{}, labels...),
		Sig:        strings.ToLower(strings.TrimSpace(*input.Sig)),
		Size:       int64(len(raw)),
	}, nil
}

func parseEventPublicKey(owner string) (ed25519.PublicKey, error) {
	const prefix = "ed25519:"
	if !strings.HasPrefix(owner, prefix) {
		return nil, fmt.Errorf("owner must be ed25519:<64 hex characters>")
	}
	publicKey, err := hex.DecodeString(strings.ToLower(strings.TrimPrefix(owner, prefix)))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("owner must be ed25519:<64 hex characters>")
	}
	return ed25519.PublicKey(publicKey), nil
}

func normalizeEventBlob(blob string) (string, error) {
	blob = strings.ToLower(strings.TrimSpace(blob))
	decoded, err := hex.DecodeString(blob)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("must be a 64 character SHA-256 hex string")
	}
	return blob, nil
}

func eventSigningMessage(owner, collection string, createdAt, expiresAt int64, canonicalData []byte, blob string, labels []string) []byte {
	var message strings.Builder
	message.WriteString(owner)
	message.WriteByte(':')
	message.WriteString(collection)
	message.WriteByte(':')
	message.WriteString(strconv.FormatInt(createdAt, 10))
	message.WriteByte(':')
	message.WriteString(strconv.FormatInt(expiresAt, 10))
	message.WriteByte(':')
	message.Write(canonicalData)
	message.WriteByte(':')
	message.WriteString(blob)
	message.WriteByte(':')
	message.WriteString(strings.Join(labels, ","))
	return []byte(message.String())
}

func canonicalJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("multiple JSON values")
	}
	return marshalCanonicalJSON(value), nil
}

func marshalCanonicalJSON(value any) []byte {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var output bytes.Buffer
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key)
			output.Write(encodedKey)
			output.WriteByte(':')
			output.Write(marshalCanonicalJSON(typed[key]))
		}
		output.WriteByte('}')
		return output.Bytes()
	case []any:
		var output bytes.Buffer
		output.WriteByte('[')
		for index, element := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			output.Write(marshalCanonicalJSON(element))
		}
		output.WriteByte(']')
		return output.Bytes()
	default:
		encoded, _ := json.Marshal(typed)
		return encoded
	}
}

func parseEventFilter(values url.Values) (eventFilter, error) {
	filter := eventFilter{
		Owner:      strings.ToLower(values.Get("owner")),
		Collection: values.Get("collection"),
		Label:      values.Get("label"),
		Search:     values.Get("search"),
		Limit:      defaultEventLimit,
	}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxEventLimit {
			return eventFilter{}, fmt.Errorf("limit must be between 1 and %d", maxEventLimit)
		}
		filter.Limit = limit
	}
	if value := values.Get("since"); value != "" {
		since, err := strconv.ParseInt(value, 10, 64)
		if err != nil || since < 0 {
			return eventFilter{}, fmt.Errorf("invalid since")
		}
		filter.Since = since
	}
	if value := values.Get("until"); value != "" {
		until, err := strconv.ParseInt(value, 10, 64)
		if err != nil || until < 0 {
			return eventFilter{}, fmt.Errorf("invalid until")
		}
		filter.Until = until
	}
	if filter.Since > 0 && filter.Until > 0 && filter.Since > filter.Until {
		return eventFilter{}, fmt.Errorf("since must not be greater than until")
	}
	if value := values.Get("blob"); value != "" {
		blob, err := normalizeEventBlob(value)
		if err != nil {
			return eventFilter{}, fmt.Errorf("invalid blob: %w", err)
		}
		filter.Blob = blob
	}
	if value := values.Get("cursor"); value != "" {
		parts := strings.SplitN(value, ":", 2)
		if len(parts) != 2 {
			return eventFilter{}, fmt.Errorf("invalid cursor")
		}
		createdAt, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || createdAt < 1 || parts[1] == "" {
			return eventFilter{}, fmt.Errorf("invalid cursor")
		}
		filter.AfterCreated = createdAt
		filter.AfterID = parts[1]
	}
	return filter, nil
}

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/events":
		if r.Method == http.MethodPost {
			h.publish(w, r)
			return
		}
		h.list(w, r)
	case r.URL.Path == "/events/stream":
		h.stream(w, r)
	default:
		h.get(w, r)
	}
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "content type must be application/json"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxEventSize+1)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]string{"error": "cannot read event body"})
		return
	}
	if len(raw) > MaxEventSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "event exceeds 8 KiB"})
		return
	}

	event, err := validateEvent(raw, time.Now())
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "bad sig") {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	created, storedAt, err := h.store.insert(event, time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot store event"})
		return
	}
	status := http.StatusCreated
	response := map[string]any{"status": "success", "id": event.ID, "stored_at": storedAt}
	if !created {
		status = http.StatusOK
		response["duplicate"] = true
	}
	writeJSON(w, status, response)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	filter, err := parseEventFilter(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	events, nextCursor := h.store.query(filter, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "success",
		"events":      events,
		"records":     events,
		"limit":       filter.Limit,
		"cursor":      r.URL.Query().Get("cursor"),
		"next_cursor": nextCursor,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/events/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
		return
	}
	event, ok := h.store.get(id, time.Now())
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "event": event, "record": event})
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	filter, err := parseEventFilter(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	subscriber, ok := h.store.subscribe(filter)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many event stream subscribers"})
		return
	}
	defer h.store.unsubscribe(subscriber)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeFrame := func(frame []byte) bool {
		if _, err := w.Write(frame); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	// Advertise a reconnect delay so a dropped connection resumes promptly.
	if !writeFrame([]byte(fmt.Sprintf("retry: %d\n: connected\n\n", sseRetryInterval.Milliseconds()))) {
		return
	}

	// Replay whatever was published while the client was disconnected. The
	// subscriber is already attached, so nothing published from here on can be
	// missed; replayed IDs are recorded to avoid delivering them twice.
	replayed := make(map[string]struct{})
	if anchor := strings.TrimSpace(r.Header.Get("Last-Event-ID")); anchor != "" {
		if backlog, ok := h.store.replayAfter(anchor, filter, time.Now()); ok {
			for _, event := range backlog {
				replayed[event.ID] = struct{}{}
				if !writeFrame(sseFrame(event)) {
					return
				}
			}
		}
	}

	ticker := time.NewTicker(sseKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !writeFrame([]byte(": keepalive\n\n")) {
				return
			}
		case event := <-subscriber.ch:
			if _, duplicate := replayed[event.ID]; duplicate {
				continue
			}
			if !writeFrame(sseFrame(event)) {
				return
			}
		}
	}
}

func sseFrame(event *Event) []byte {
	data, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return []byte("event: event\nid: " + event.ID + "\ndata: " + string(data) + "\n\n")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode JSON response: %v", err)
	}
}

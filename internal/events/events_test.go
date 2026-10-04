package events

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/besoeasy/originless/internal/testutil"
)

func publishTestEvent(t *testing.T, router http.Handler, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.PublishTestEvent(t, router, raw)
}

type eventPublishResponse struct {
	Status    string `json:"status"`
	ID        string `json:"id"`
	StoredAt  string `json:"stored_at"`
	Duplicate bool   `json:"duplicate"`
}

func TestSignedEventLifecycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	createdAt := now.Unix() - 5
	expiresAt := now.Unix() + 3600
	raw, owner := testutil.MakeSignedEvent(t, createdAt, expiresAt, "chat", map[string]any{
		"user":    "alice",
		"message": "Hello world!",
	}, []string{"room:lobby"}, "")
	router := NewHandler(newTestStore(t))

	recorder := publishTestEvent(t, router, raw)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("publish status = %d, want %d; body = %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	var published eventPublishResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &published); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	if published.Status != "success" || published.ID == "" || published.StoredAt == "" {
		t.Fatalf("publish response = %+v, want success with id and stored_at", published)
	}

	getRecorder := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/events/"+published.ID, nil)
	router.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body = %s", getRecorder.Code, http.StatusOK, getRecorder.Body.String())
	}
	var got struct {
		Event *Event `json:"event"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.Event == nil || got.Event.ID != published.ID || got.Event.Owner != owner || got.Event.Collection != "chat" {
		t.Fatalf("event = %+v, want published event", got.Event)
	}
	if string(got.Event.Data) != `{"message":"Hello world!","user":"alice"}` {
		t.Errorf("canonical data = %s, want sorted compact JSON", got.Event.Data)
	}

	queryRecorder := httptest.NewRecorder()
	queryRequest := httptest.NewRequest(http.MethodGet, "/events?collection=chat&label=room:lobby&owner="+owner, nil)
	router.ServeHTTP(queryRecorder, queryRequest)
	if queryRecorder.Code != http.StatusOK {
		t.Fatalf("query status = %d, want %d", queryRecorder.Code, http.StatusOK)
	}
	var query struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(queryRecorder.Body.Bytes(), &query); err != nil {
		t.Fatalf("decode query response: %v", err)
	}
	if len(query.Events) != 1 || query.Events[0].ID != published.ID {
		t.Errorf("query events = %+v, want published event", query.Events)
	}

	duplicateRecorder := publishTestEvent(t, router, raw)
	if duplicateRecorder.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, want %d", duplicateRecorder.Code, http.StatusOK)
	}
	var duplicate eventPublishResponse
	if err := json.Unmarshal(duplicateRecorder.Body.Bytes(), &duplicate); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}
	if !duplicate.Duplicate || duplicate.ID != published.ID {
		t.Errorf("duplicate response = %+v, want duplicate success", duplicate)
	}
}

// Expired events are never served, and they stay resident until the hourly
// reaper reclaims them rather than being deleted by a read.
func TestExpiredEventsAreNeverServed(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix()-100, now.Unix()-1, "chat", map[string]any{"message": "expired"}, []string{}, "")
	store := newTestStore(t)
	handler := NewHandler(store)
	publishedID := eventIDFromResponse(t, publishTestEvent(t, handler, raw))

	for _, path := range []string{
		"/events",
		"/events?collection=chat",
		"/events/" + publishedID,
		"/events/" + publishedID + "?include_expired=true",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(recorder.Body.String(), publishedID) {
			t.Errorf("%s exposed an expired event: %s", path, recorder.Body.String())
		}
	}

	byID := httptest.NewRecorder()
	handler.ServeHTTP(byID, httptest.NewRequest(http.MethodGet, "/events/"+publishedID, nil))
	if byID.Code != http.StatusNotFound {
		t.Errorf("expired get status = %d, want %d", byID.Code, http.StatusNotFound)
	}

	// Reads must not have deleted it; the reaper owns deletion.
	if stats := store.Stats(now); stats.Expired != 1 || stats.Total != 1 {
		t.Errorf("stats after reads = %+v, want the expired event still resident", stats)
	}
	if removed := store.Reap(now); removed != 1 {
		t.Errorf("Reap() removed %d events, want 1", removed)
	}
	if stats := store.Stats(now); stats.Total != 0 {
		t.Errorf("stats after reap = %+v, want the store empty", stats)
	}
}

func TestStoreReaperRemovesExpiredEvents(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix()-100, now.Unix()-1, "chat", map[string]any{"message": "expired"}, []string{}, "")
	event, err := validateEvent(raw, now)
	if err != nil {
		t.Fatalf("validate event: %v", err)
	}
	store.insert(event, now)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.StartReaper(ctx, 10*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.Stats(now).Total == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("reaper did not remove the expired event")
}

// A page that exactly fills the limit is the last page, so no cursor is
// returned and clients do not make a pointless trailing request.
func TestEventQueryOmitsCursorOnLastPage(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	store := newTestStore(t)
	handler := NewHandler(store)
	for i := 0; i < 3; i++ {
		raw, _ := testutil.MakeSignedEvent(t, now.Unix()-100, now.Unix()+3600, "chat", map[string]any{"i": i}, []string{}, "")
		publishTestEvent(t, handler, raw)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/events?collection=chat&limit=3", nil))
	var result struct {
		Events     []Event `json:"events"`
		NextCursor string  `json:"next_cursor"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(result.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(result.Events))
	}
	if result.NextCursor != "" {
		t.Errorf("next_cursor = %q, want empty on the final page", result.NextCursor)
	}

	partial := httptest.NewRecorder()
	handler.ServeHTTP(partial, httptest.NewRequest(http.MethodGet, "/events?collection=chat&limit=2", nil))
	var first struct {
		Events     []Event `json:"events"`
		NextCursor string  `json:"next_cursor"`
	}
	if err := json.Unmarshal(partial.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode partial page: %v", err)
	}
	if len(first.Events) != 2 || first.NextCursor == "" {
		t.Fatalf("partial page = %d events, cursor %q; want 2 events and a cursor", len(first.Events), first.NextCursor)
	}
}

// The owner is canonicalized to lower case so one key always produces one ID
// and matches an ?owner= filter regardless of the case used to query it.
func TestEventOwnerIsCaseNormalized(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	raw, owner := testutil.MakeSignedEvent(t, now.Unix()-10, now.Unix()+3600, "chat", map[string]any{"message": "case"}, []string{}, "")
	if strings.ToLower(owner) != owner {
		t.Fatalf("testutil owner %q is not lower case", owner)
	}

	store := newTestStore(t)
	handler := NewHandler(store)
	event, err := validateEvent(raw, now)
	if err != nil {
		t.Fatalf("validate event: %v", err)
	}
	if event.Owner != owner {
		t.Errorf("owner = %q, want lower case %q", event.Owner, owner)
	}
	publishTestEvent(t, handler, raw)

	upperRecorder := httptest.NewRecorder()
	handler.ServeHTTP(upperRecorder, httptest.NewRequest(http.MethodGet, "/events?owner="+strings.ToUpper(owner), nil))
	if !strings.Contains(upperRecorder.Body.String(), event.ID) {
		t.Errorf("upper case ?owner= did not match: %s", upperRecorder.Body.String())
	}
	if stats := store.Stats(now); stats.UniqueOwners != 1 {
		t.Errorf("unique_owners = %d, want 1", stats.UniqueOwners)
	}
}

func eventIDFromResponse(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response eventPublishResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	return response.ID
}

func TestEventQueryCursorAndFilters(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	store := newTestStore(t)
	handler := NewHandler(store)
	first, _ := testutil.MakeSignedEvent(t, now.Unix()-20, now.Unix()+3600, "chat", map[string]any{"message": "first"}, []string{"room:lobby"}, "")
	second, _ := testutil.MakeSignedEvent(t, now.Unix()-10, now.Unix()+3600, "chat", map[string]any{"message": "second"}, []string{"room:lobby"}, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	publishTestEvent(t, handler, first)
	secondRecorder := publishTestEvent(t, handler, second)
	var secondPublished eventPublishResponse
	if err := json.Unmarshal(secondRecorder.Body.Bytes(), &secondPublished); err != nil {
		t.Fatalf("decode second publish response: %v", err)
	}

	firstPage := httptest.NewRecorder()
	handler.ServeHTTP(firstPage, httptest.NewRequest(http.MethodGet, "/events?collection=chat&limit=1", nil))
	var page struct {
		Events     []Event `json:"events"`
		NextCursor string  `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstPage.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(page.Events) != 1 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, want one event and cursor", page)
	}

	secondPage := httptest.NewRecorder()
	handler.ServeHTTP(secondPage, httptest.NewRequest(http.MethodGet, "/events?collection=chat&limit=1&cursor="+page.NextCursor, nil))
	var secondResult struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(secondPage.Body.Bytes(), &secondResult); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(secondResult.Events) != 1 || secondResult.Events[0].ID == page.Events[0].ID {
		t.Errorf("second page = %+v, want a different event", secondResult.Events)
	}

	blobPage := httptest.NewRecorder()
	handler.ServeHTTP(blobPage, httptest.NewRequest(http.MethodGet, "/events?blob=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil))
	if !strings.Contains(blobPage.Body.String(), secondPublished.ID) {
		t.Errorf("blob filter response = %s, want second event", blobPage.Body.String())
	}
}

func TestEventValidationRejectsBadSignatureAndTTL(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix(), now.Unix()+3600, "chat", map[string]any{"message": "hello"}, []string{}, "")
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	payload["data"] = map[string]any{"message": "tampered"}
	tampered, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal tampered event: %v", err)
	}
	router := NewHandler(newTestStore(t))
	badSignature := publishTestEvent(t, router, tampered)
	if badSignature.Code != http.StatusUnauthorized {
		t.Errorf("tampered status = %d, want %d", badSignature.Code, http.StatusUnauthorized)
	}

	tooLong, _ := testutil.MakeSignedEvent(t, now.Unix(), now.Unix()+MaxEventTTL+1, "chat", map[string]any{"message": "hello"}, []string{}, "")
	tooLong = append(tooLong, bytes.Repeat([]byte(" "), MaxEventSize)...)
	tooLongRecorder := publishTestEvent(t, router, tooLong)
	if tooLongRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized status = %d, want %d", tooLongRecorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestEventStreamEndpoint(t *testing.T) {
	store := newTestStore(t)
	handler := NewHandler(store)
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events/stream?collection=chat", nil)
	if err != nil {
		t.Fatalf("create stream request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("stream content type = %q, want text/event-stream", contentType)
	}

	reader := bufio.NewReader(response.Body)
	retry, err := reader.ReadString('\n')
	if err != nil || retry != fmt.Sprintf("retry: %d\n", sseRetryInterval.Milliseconds()) {
		t.Fatalf("retry frame = %q, error = %v", retry, err)
	}
	connected, err := reader.ReadString('\n')
	if err != nil || connected != ": connected\n" {
		t.Fatalf("connected frame = %q, error = %v", connected, err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read connected frame terminator: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix(), now.Unix()+3600, "chat", map[string]any{"message": "streamed"}, []string{}, "")
	publishTestEvent(t, handler, raw)

	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, "event: event") || !strings.Contains(frame, "data: {") {
		t.Errorf("event frame = %q, want event and JSON data", frame)
	}
}

// readSSEFrame reads frames until one carries data, skipping retry, connected
// and keepalive comment lines, and returns the raw frame text.
func readSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	for {
		var frame strings.Builder
		sawData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read event frame: %v (frame so far %q)", err, frame.String())
			}
			if line == "\n" {
				break
			}
			if strings.HasPrefix(line, "data: ") {
				sawData = true
			}
			frame.WriteString(line)
		}
		if sawData {
			return frame.String()
		}
	}
}

// A client that reconnects with Last-Event-ID receives everything published
// while it was away instead of silently losing those events.
func TestEventStreamResumesFromLastEventID(t *testing.T) {
	store := newTestStore(t)
	handler := NewHandler(store)
	server := httptest.NewServer(handler)
	defer server.Close()

	open := func(lastEventID string) (*http.Response, *bufio.Reader, context.CancelFunc) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events/stream?collection=chat", nil)
		if err != nil {
			cancel()
			t.Fatalf("create stream request: %v", err)
		}
		if lastEventID != "" {
			request.Header.Set("Last-Event-ID", lastEventID)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			cancel()
			t.Fatalf("open event stream: %v", err)
		}
		return response, bufio.NewReader(response.Body), cancel
	}

	now := time.Now().Truncate(time.Second)

	// Attach, then publish, so the first event arrives over the live stream.
	response, reader, cancelFirst := open("")
	defer cancelFirst()
	first, _ := testutil.MakeSignedEvent(t, now.Unix()-20, now.Unix()+3600, "chat", map[string]any{"message": "before"}, []string{}, "")
	firstID := eventIDFromResponse(t, publishTestEvent(t, handler, first))
	if frame := readSSEFrame(t, reader); !strings.Contains(frame, firstID) {
		t.Fatalf("live frame = %q, want %s", frame, firstID)
	}
	response.Body.Close()

	// Published while nobody is listening.
	missed, _ := testutil.MakeSignedEvent(t, now.Unix()-10, now.Unix()+3600, "chat", map[string]any{"message": "missed"}, []string{}, "")
	missedID := eventIDFromResponse(t, publishTestEvent(t, handler, missed))

	resumed, resumedReader, cancelResumed := open(firstID)
	defer cancelResumed()
	defer resumed.Body.Close()
	frame := readSSEFrame(t, resumedReader)
	if !strings.Contains(frame, missedID) {
		t.Errorf("resumed frame = %q, want the event published while disconnected (%s)", frame, missedID)
	}
	if strings.Contains(frame, firstID) {
		t.Errorf("resumed frame replayed the anchor event itself: %q", frame)
	}
}

// An unknown Last-Event-ID must not break the stream; the client simply
// continues with live events.
func TestEventStreamIgnoresUnknownLastEventID(t *testing.T) {
	store := newTestStore(t)
	handler := NewHandler(store)
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events/stream?collection=chat", nil)
	if err != nil {
		t.Fatalf("create stream request: %v", err)
	}
	request.Header.Set("Last-Event-ID", "does-not-exist")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	reader := bufio.NewReader(response.Body)
	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix(), now.Unix()+3600, "chat", map[string]any{"message": "live"}, []string{}, "")
	publishTestEvent(t, handler, raw)
	if frame := readSSEFrame(t, reader); !strings.Contains(frame, "event: event") {
		t.Errorf("frame = %q, want a live event", frame)
	}
}

func TestEventStoreBroadcastsToMatchingSubscriber(t *testing.T) {
	store := newTestStore(t)
	subscriber, ok := store.subscribe(eventFilter{Collection: "chat"})
	if !ok {
		t.Fatal("subscribe returned false")
	}
	defer store.unsubscribe(subscriber)

	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix(), now.Unix()+3600, "chat", map[string]any{"message": "streamed"}, []string{}, "")
	publishTestEvent(t, NewHandler(store), raw)

	select {
	case event := <-subscriber.ch:
		if event.Collection != "chat" {
			t.Errorf("broadcast collection = %q, want chat", event.Collection)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast event")
	}
}

func TestPersistentStoreSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/events.db"

	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix()-5, now.Unix()+3600, "chat", map[string]any{"message": "durable"}, []string{}, "")
	event, err := validateEvent(raw, now)
	if err != nil {
		t.Fatalf("validateEvent: %v", err)
	}

	store, err := NewStoreAt(path, 0)
	if err != nil {
		t.Fatalf("NewPersistentStore: %v", err)
	}
	if _, _, err := store.insert(event, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewStoreAt(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, ok := reopened.get(event.ID, now)
	if !ok {
		t.Fatal("event missing after reopen")
	}
	if got.Collection != "chat" || got.ID != event.ID || got.StoredAt == "" {
		t.Errorf("reopened event = %+v, want the stored event", got)
	}
}

func TestPersistentStoreReapsExpiredRows(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/events.db"

	now := time.Now().Truncate(time.Second)
	raw, _ := testutil.MakeSignedEvent(t, now.Unix()-7200, now.Unix()-3600, "chat", map[string]any{"message": "old"}, []string{}, "")
	event, err := validateEvent(raw, now.Add(-7200*time.Second))
	if err != nil {
		t.Fatalf("validateEvent: %v", err)
	}

	store, err := NewStoreAt(path, 0)
	if err != nil {
		t.Fatalf("NewPersistentStore: %v", err)
	}
	if _, _, err := store.insert(event, now.Add(-7200*time.Second)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewStoreAt(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if _, ok := reopened.get(event.ID, now); ok {
		t.Error("expired event served after reopen")
	}
	if stats := reopened.Stats(now); stats.Total != 0 {
		t.Errorf("total = %d, want 0 after expired rows are purged", stats.Total)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStoreAt(filepath.Join(t.TempDir(), "events.db"), 0)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

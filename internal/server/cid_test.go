package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/besoeasy/originless/internal/ipfs"
)

type cidTestServer struct {
	server       *httptest.Server
	numLinks     int64
	catContent   string
	blockStatus  int
	objectStatus int
	// modern mimics current Kubo: object/stat has been removed and files/stat
	// answers instead. When false the node behaves like the older release and
	// only serves object/stat, which is the fallback path.
	modern     bool
	filesType  string
	filesSize  int64
	requests   []string
	blockCalls int
}

func newCIDTestServer(t *testing.T) *cidTestServer {
	t.Helper()
	s := &cidTestServer{
		blockStatus:  http.StatusOK,
		objectStatus: http.StatusOK,
		catContent:   `{"hello":"world"}`,
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v0/block/stat":
			s.blockCalls++
			if s.blockStatus != http.StatusOK {
				http.Error(w, "block was not found locally (offline): ipld: could not find "+r.URL.Query().Get("arg"), s.blockStatus)
				return
			}
			_, _ = fmt.Fprintf(w, `{"Key":%q,"Size":74}`, r.URL.Query().Get("arg"))
		case "/api/v0/object/stat":
			if s.modern {
				http.Error(w, `{"Message":"removed, use 'ipfs dag' or 'ipfs files' instead","Code":0,"Type":"error"}`, http.StatusInternalServerError)
				return
			}
			if s.objectStatus != http.StatusOK {
				http.Error(w, "not found", s.objectStatus)
				return
			}
			_, _ = fmt.Fprintf(w, `{"Hash":%q,"NumLinks":%d,"BlockSize":74,"LinksSize":2,"DataSize":72,"CumulativeSize":74}`,
				r.URL.Query().Get("arg"), s.numLinks)
		case "/api/v0/files/stat":
			if !s.modern {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprintf(w, `{"Hash":%q,"Size":%d,"CumulativeSize":74,"Blocks":1,"Type":%q}`,
				r.URL.Query().Get("arg"), s.filesSize, s.filesType)
		case "/api/v0/ls":
			_, _ = fmt.Fprintf(w, `{"Objects":[{"Hash":%q,"Links":[`+
				`{"Name":"a.txt","Hash":"Qma","Size":10,"Type":2},`+
				`{"Name":"dir","Hash":"Qmb","Size":0,"Type":1}]}]}`, r.URL.Query().Get("arg"))
		case "/api/v0/cat":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte(s.catContent))
		default:
			http.NotFound(w, r)
		}
	}))
	return s
}

func (s *cidTestServer) close() {
	s.server.Close()
}

func TestCIDInfoAvailableFileWithJSON(t *testing.T) {
	upstream := newCIDTestServer(t)
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-file", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response cidResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Available {
		t.Errorf("available = false, want true")
	}
	if response.CID != "bafy-file" || response.Error != "" {
		t.Errorf("response = %+v, want cid bafy-file and no error", response)
	}
	if response.Block == nil || response.Block.Key != "bafy-file" || response.Block.Size != 74 {
		t.Errorf("block = %+v, want bafy-file size 74", response.Block)
	}
	if response.Object == nil || response.Object.NumLinks != 0 {
		t.Errorf("object = %+v, want a file object", response.Object)
	}
	if response.Links != nil {
		t.Errorf("links = %+v, want none for a file", response.Links)
	}
	if want := base64.StdEncoding.EncodeToString([]byte(`{"hello":"world"}`)); response.Data != want {
		t.Errorf("data = %q, want %q", response.Data, want)
	}
	if len(response.JSON) == 0 || string(response.JSON) != `{"hello":"world"}` {
		t.Errorf("json = %s, want decoded file content", response.JSON)
	}
}

func TestCIDInfoAvailableDirectory(t *testing.T) {
	upstream := newCIDTestServer(t)
	upstream.numLinks = 2
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-dir", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response cidResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Available {
		t.Errorf("available = false, want true")
	}
	if response.Object == nil || response.Object.NumLinks != 2 {
		t.Errorf("object = %+v, want a directory object with two links", response.Object)
	}
	if len(response.Links) != 2 {
		t.Fatalf("links = %v, want two directory entries", response.Links)
	}
	if response.Links[0].Name != "a.txt" || response.Links[0].Hash != "Qma" || response.Links[1].Type != 1 {
		t.Errorf("links = %+v, want expected directory entries", response.Links)
	}
	if response.Data != "" || len(response.JSON) != 0 {
		t.Errorf("data = %q, json = %s; want no content for a directory", response.Data, response.JSON)
	}
}

func TestCIDInfoUnavailable(t *testing.T) {
	upstream := newCIDTestServer(t)
	upstream.blockStatus = http.StatusInternalServerError
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-missing", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response cidResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Available {
		t.Errorf("available = true, want false for a missing CID")
	}
	if !strings.Contains(response.Error, "unavailable") {
		t.Errorf("error = %q, want unavailable message", response.Error)
	}
	if response.Block != nil || response.Object != nil {
		t.Errorf("response = %+v, want no stats for a missing CID", response)
	}
	// A missing block must not trigger any further lookups.
	if upstream.blockCalls != 1 {
		t.Errorf("block/stat called %d times, want 1", upstream.blockCalls)
	}
	for _, request := range upstream.requests {
		if strings.Contains(request, "object/stat") || strings.Contains(request, "files/stat") {
			t.Errorf("request %q was made for a missing CID", request)
		}
	}
}

// Current Kubo removed object/stat in favour of files/stat, so a file CID must
// still be described (and its content returned) on a node that serves only the
// modern endpoint.
func TestCIDInfoAgainstModernKubo(t *testing.T) {
	upstream := newCIDTestServer(t)
	upstream.modern = true
	upstream.filesType = "file"
	upstream.filesSize = 17
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-modern", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response cidResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Available {
		t.Errorf("available = false, want true")
	}
	if response.Object == nil {
		t.Fatalf("object = nil, want a UnixFS stat from files/stat")
	}
	if response.Object.Type != "file" || response.Object.Blocks != 1 || response.Object.DataSize != 17 {
		t.Errorf("object = %+v, want files/stat values", response.Object)
	}
	if response.Object.IsDirectory() {
		t.Errorf("IsDirectory() = true for a file")
	}
	if want := base64.StdEncoding.EncodeToString([]byte(`{"hello":"world"}`)); response.Data != want {
		t.Errorf("data = %q, want %q", response.Data, want)
	}
	if string(response.JSON) != `{"hello":"world"}` {
		t.Errorf("json = %s, want decoded file content", response.JSON)
	}
}

// A modern node reports directories through files/stat's Type field and
// reports no NumLinks at all, so the link listing must key off Type.
func TestCIDInfoListsLinksForModernDirectory(t *testing.T) {
	upstream := newCIDTestServer(t)
	upstream.modern = true
	upstream.filesType = "directory"
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-modern-dir", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response cidResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Object == nil || !response.Object.IsDirectory() {
		t.Fatalf("object = %+v, want a directory", response.Object)
	}
	if len(response.Links) != 2 || response.Links[0].Name != "a.txt" {
		t.Errorf("links = %+v, want two directory entries", response.Links)
	}
	if response.Data != "" || len(response.JSON) != 0 {
		t.Errorf("data = %q, json = %s; want no content for a directory", response.Data, response.JSON)
	}
}

// Every CID lookup describes the local repository. Without offline=true Kubo
// tries to fetch an absent block from the network, which turns a local
// availability check into a multi-second stall before a misleading 503.
func TestCIDLookupsAreOffline(t *testing.T) {
	for _, modern := range []bool{false, true} {
		upstream := newCIDTestServer(t)
		upstream.modern = modern
		upstream.filesType = "directory"

		client, err := ipfs.NewClient(upstream.server.URL)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		recorder := httptest.NewRecorder()
		mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-offline", nil))

		if len(upstream.requests) == 0 {
			t.Fatalf("modern=%t: no upstream requests recorded", modern)
		}
		for _, request := range upstream.requests {
			if !strings.Contains(request, "offline=true") {
				t.Errorf("modern=%t: request %q is not offline", modern, request)
			}
		}
		upstream.close()
	}
}

func TestCIDInfoNodeUnavailable(t *testing.T) {
	client, err := ipfs.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cid/bafy-any", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(recorder.Body.String(), "IPFS node unavailable") {
		t.Errorf("body = %q, want IPFS node unavailable", recorder.Body.String())
	}
}

func TestCIDInfoMethodNotAllowed(t *testing.T) {
	upstream := newCIDTestServer(t)
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/cid/bafy-file", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if len(upstream.requests) != 0 {
		t.Errorf("upstream requests = %v, want none for a rejected method", upstream.requests)
	}
}

func TestCIDClientMethods(t *testing.T) {
	upstream := newCIDTestServer(t)
	upstream.numLinks = 2
	defer upstream.close()

	client, err := ipfs.NewClient(upstream.server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	block, err := client.BlockStat(t.Context(), "bafy-one")
	if err != nil {
		t.Fatalf("BlockStat() error = %v", err)
	}
	if block.Key != "bafy-one" || block.Size != 74 {
		t.Errorf("block = %+v, want bafy-one size 74", block)
	}

	obj, err := client.ObjectStat(t.Context(), "bafy-one")
	if err != nil {
		t.Fatalf("ObjectStat() error = %v", err)
	}
	if obj.Hash != "bafy-one" || obj.NumLinks != 2 {
		t.Errorf("object = %+v, want bafy-one with two links", obj)
	}

	links, err := client.Ls(t.Context(), "bafy-one")
	if err != nil {
		t.Fatalf("Ls() error = %v", err)
	}
	if len(links) != 2 || links[0].Name != "a.txt" {
		t.Errorf("links = %+v, want two directory entries", links)
	}

	data, decoded, err := client.Content(t.Context(), "bafy-one")
	if err != nil {
		t.Fatalf("Content() error = %v", err)
	}
	if string(data) != `{"hello":"world"}` {
		t.Errorf("content = %q, want JSON file body", data)
	}
	if string(decoded) != `{"hello":"world"}` {
		t.Errorf("decoded = %s, want parsed JSON", decoded)
	}
}

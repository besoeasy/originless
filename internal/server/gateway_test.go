package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/besoeasy/originless/internal/config"
	"github.com/besoeasy/originless/internal/ipfs"
)

func gatewayTestRouter(t *testing.T, gatewayURL string) http.Handler {
	t.Helper()
	client, err := ipfs.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	cfg := config.Default()
	cfg.GatewayURL = gatewayURL
	return mustRouter(t, context.Background(), client, cfg)
}

type gatewayRequest struct {
	method string
	path   string
	query  string
	header http.Header
}

func TestGatewayProxiesFileRequests(t *testing.T) {
	var mu sync.Mutex
	var captured gatewayRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = gatewayRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			header: r.Header.Clone(),
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Range", "bytes 0-4/11")
		w.Header().Set("ETag", `"bafy-etag"`)
		w.Header().Set("Cache-Control", "public, max-age=29030400, immutable")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("hello"))
	}))
	defer upstream.Close()

	request := httptest.NewRequest(http.MethodGet, "/ipfs/bafy-file/hello.txt?download=true&filename=hello.txt", nil)
	request.Header.Set("Range", "bytes=0-4")
	request.Header.Set("If-None-Match", `"bafy-etag"`)
	recorder := httptest.NewRecorder()
	gatewayTestRouter(t, upstream.URL).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusPartialContent, recorder.Body.String())
	}
	if body := recorder.Body.String(); body != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
	if got := recorder.Header().Get("Content-Range"); got != "bytes 0-4/11" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 0-4/11")
	}
	if got := recorder.Header().Get("ETag"); got != `"bafy-etag"` {
		t.Errorf("ETag = %q, want %q", got, `"bafy-etag"`)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=29030400, immutable" {
		t.Errorf("Cache-Control = %q, want immutable cache header", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if captured.method != http.MethodGet {
		t.Errorf("upstream method = %q, want GET", captured.method)
	}
	if captured.path != "/ipfs/bafy-file/hello.txt" {
		t.Errorf("upstream path = %q, want /ipfs/bafy-file/hello.txt", captured.path)
	}
	if captured.query != "download=true&filename=hello.txt" {
		t.Errorf("upstream query = %q, want the caller query", captured.query)
	}
	if got := captured.header.Get("Range"); got != "bytes=0-4" {
		t.Errorf("upstream Range = %q, want bytes=0-4", got)
	}
	if got := captured.header.Get("If-None-Match"); got != `"bafy-etag"` {
		t.Errorf("upstream If-None-Match = %q, want the caller ETag", got)
	}
}

func TestGatewayPreservesTrailingSlash(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		_, _ = w.Write([]byte("directory index"))
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	gatewayTestRouter(t, upstream.URL).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ipfs/bafy-dir/", nil))

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/ipfs/bafy-dir/" {
		t.Errorf("upstream path = %q, want trailing slash preserved", gotPath)
	}
}

func TestGatewaySupportsHead(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("upstream method = %q, want HEAD", r.Method)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	gatewayTestRouter(t, upstream.URL).ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "/ipfs/bafy-file", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Length"); got != "5" {
		t.Errorf("Content-Length = %q, want 5", got)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("body = %q, want no body for HEAD", recorder.Body.String())
	}
}

func TestGatewayRejectsUnsupportedMethods(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "should not be called")
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/ipfs/bafy-file", strings.NewReader("data"))
	gatewayTestRouter(t, upstream.URL).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want GET, HEAD", got)
	}
	if calls.Load() != 0 {
		t.Errorf("upstream calls = %d, want none", calls.Load())
	}
}

func TestGatewaySubpathRejectsTraversal(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{"/ipfs/bafy-file", "bafy-file", true},
		{"/ipfs/bafy-dir/nested/file.txt", "bafy-dir/nested/file.txt", true},
		{"/ipfs/bafy-dir/", "bafy-dir/", true},
		{"/ipfs/bafy-dir/nested/", "bafy-dir/nested/", true},
		{"/ipfs/bafy-dir/./file.txt", "bafy-dir/file.txt", true},
		{"/ipfs/", "", false},
		{"/ipfs", "", false},
		{"/ipfs//", "", false},
		{"/ipfs/../etc/passwd", "", false},
		{"/ipfs/bafy-dir/../../etc/passwd", "", false},
		{"/ipfs/bafy-dir/\x00file", "", false},
		{"/other/bafy-file", "", false},
	}

	for _, test := range tests {
		got, ok := gatewaySubpath(test.path)
		if ok != test.ok || got != test.want {
			t.Errorf("gatewaySubpath(%q) = %q, %v; want %q, %v", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestGatewayReturnsBadGatewayWhenNodeIsDown(t *testing.T) {
	recorder := httptest.NewRecorder()
	gatewayTestRouter(t, "http://127.0.0.1:1").ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ipfs/bafy-file", nil))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if !strings.Contains(recorder.Body.String(), "IPFS gateway unavailable") {
		t.Errorf("body = %q, want unavailable message", recorder.Body.String())
	}
}

func TestGatewayRewritesLoopbackRedirect(t *testing.T) {
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", upstreamURL+"/ipfs/bafy-dir/")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	recorder := httptest.NewRecorder()
	gatewayTestRouter(t, upstream.URL).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ipfs/bafy-dir", nil))

	if recorder.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMovedPermanently)
	}
	if got := recorder.Header().Get("Location"); got != "/ipfs/bafy-dir/" {
		t.Errorf("Location = %q, want relative gateway path", got)
	}
}

func TestGatewayStripsHopByHopHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "X-Internal")
		w.Header().Set("X-Internal", "secret")
		w.Header().Set("X-Keep", "visible")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	gatewayTestRouter(t, upstream.URL).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ipfs/bafy-file", nil))

	if got := recorder.Header().Get("Connection"); got != "" {
		t.Errorf("Connection = %q, want it stripped", got)
	}
	if got := recorder.Header().Get("X-Internal"); got != "" {
		t.Errorf("X-Internal = %q, want it stripped", got)
	}
	if got := recorder.Header().Get("X-Keep"); got != "visible" {
		t.Errorf("X-Keep = %q, want visible", got)
	}
}

// Kubo emits one line per CORS value. The proxy must replace the middleware's
// defaults with those values, not append to them, and it must not duplicate
// single-valued headers while writing the response.
func TestGatewayResponseHeadersAreNotDuplicated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Add("Access-Control-Allow-Headers", "Range")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "public, max-age=29030400, immutable")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "5")
		w.Header().Set("ETag", `"bafy-etag"`)
		w.Header().Set("X-Ipfs-Path", "/ipfs/bafy-file")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(gatewayTestRouter(t, upstream.URL))
	defer proxy.Close()

	response, err := http.Get(proxy.URL + "/ipfs/bafy-file")
	if err != nil {
		t.Fatalf("GET through gateway: %v", err)
	}
	defer response.Body.Close()

	for _, name := range []string{
		"Access-Control-Allow-Origin",
		"Cache-Control",
		"Content-Length",
		"Content-Type",
		"ETag",
		"X-Ipfs-Path",
	} {
		if values := response.Header.Values(name); len(values) != 1 {
			t.Errorf("%s values = %q, want exactly one", name, values)
		}
	}
	if values := response.Header.Values("Access-Control-Allow-Headers"); len(values) != 2 {
		t.Errorf("Access-Control-Allow-Headers values = %q, want Kubo's two values only", values)
	}
}

func TestRouterRejectsInvalidGatewayURL(t *testing.T) {
	client, err := ipfs.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	cfg := config.Default()
	cfg.GatewayURL = "file:///tmp/gateway"

	if _, err := NewRouterWithOptions(context.Background(), client, cfg); err == nil {
		t.Fatal("NewRouterWithOptions() error = nil, want invalid gateway URL error")
	}
}

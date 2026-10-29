package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	gatewayPrefix         = "/ipfs/"
	gatewayCopyBufferSize = 32 * 1024

	// The gateway is always the Kubo daemon in this container. Only tests
	// construct a handler with another URL.
	defaultGatewayURL = "http://127.0.0.1:8080"
)

// gatewayHandler serves /ipfs/{cid}[/path] by proxying to the Kubo gateway
// that runs beside the app. Kubo already implements everything a gateway
// needs: directory indexes, index.html resolution, Range requests, ETags and
// content-type sniffing. Proxying keeps one public port while reusing it.
type gatewayHandler struct {
	upstream *url.URL
	client   *http.Client
}

func newGatewayHandler(rawURL string) (*gatewayHandler, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = defaultGatewayURL
	}

	upstream, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse IPFS gateway URL: %w", err)
	}
	if (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
		return nil, fmt.Errorf("IPFS gateway URL must be an http(s) URL")
	}

	return &gatewayHandler{
		upstream: upstream,
		client: &http.Client{
			// A gateway's redirects (directory trailing slash, subdomain
			// gateways) belong to the caller, not to the proxy.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       60 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   100,
			},
		},
	}, nil
}

func (h *gatewayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed",
		})
		return
	}

	subpath, ok := gatewaySubpath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	target := *h.upstream
	target.Path = strings.TrimRight(h.upstream.Path, "/") + gatewayPrefix + subpath
	target.RawQuery = r.URL.RawQuery

	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
	if err != nil {
		log.Printf("create IPFS gateway request for %s: %v", r.URL.Path, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "IPFS gateway unavailable",
		})
		return
	}
	copyGatewayRequestHeaders(request.Header, r.Header)

	response, err := h.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The caller went away; there is nobody left to answer.
			return
		}
		log.Printf("IPFS gateway request failed for %s: %v", r.URL.Path, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "IPFS gateway unavailable",
		})
		return
	}
	defer response.Body.Close()

	copyGatewayResponseHeaders(w.Header(), response.Header)
	h.rewriteLocation(w.Header())
	w.WriteHeader(response.StatusCode)

	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.CopyBuffer(w, response.Body, make([]byte, gatewayCopyBufferSize)); err != nil {
		// Headers are already committed, so the only thing left is to log.
		// Broken pipes and client disconnects are routine for large files.
		log.Printf("stream IPFS gateway response for %s: %v", r.URL.Path, err)
	}
}

// gatewaySubpath validates the part after /ipfs/ and rejects traversal before
// anything reaches the local gateway. CIDs and file names may contain many
// characters, so only dot segments and NUL are rejected.
func gatewaySubpath(requestPath string) (string, bool) {
	if !strings.HasPrefix(requestPath, gatewayPrefix) {
		return "", false
	}
	subpath := strings.TrimPrefix(requestPath, gatewayPrefix)
	if subpath == "" || strings.ContainsRune(subpath, '\x00') {
		return "", false
	}
	for _, segment := range strings.Split(subpath, "/") {
		if segment == ".." {
			return "", false
		}
	}

	cleaned := path.Clean(subpath)
	if cleaned == "." || cleaned == "/" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	// A trailing slash is meaningful to the gateway: it selects the
	// directory index instead of causing another directory redirect.
	if strings.HasSuffix(subpath, "/") {
		cleaned += "/"
	}
	return cleaned, true
}

var gatewayRequestHeaders = []string{
	"Accept",
	"Accept-Encoding",
	"If-Match",
	"If-Modified-Since",
	"If-None-Match",
	"If-Range",
	"If-Unmodified-Since",
	"Range",
}

func copyGatewayRequestHeaders(destination, source http.Header) {
	for _, name := range gatewayRequestHeaders {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

var gatewayHopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func copyGatewayResponseHeaders(destination, source http.Header) {
	skip := make(map[string]struct{}, len(gatewayHopByHopHeaders))
	for _, name := range gatewayHopByHopHeaders {
		skip[name] = struct{}{}
	}
	// A Connection header can name additional headers that are hop-by-hop.
	for _, value := range source.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				skip[http.CanonicalHeaderKey(name)] = struct{}{}
			}
		}
	}

	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if _, ok := skip[canonical]; ok {
			continue
		}
		destination[canonical] = append([]string(nil), values...)
	}
}

// rewriteLocation turns a redirect that points at the loopback gateway into a
// relative one, so a browser follows it through the public Originless port
// instead of trying to reach 127.0.0.1:8080 itself.
func (h *gatewayHandler) rewriteLocation(header http.Header) {
	location := header.Get("Location")
	if location == "" {
		return
	}
	parsed, err := url.Parse(location)
	if err != nil || !parsed.IsAbs() {
		return
	}
	if !strings.EqualFold(parsed.Scheme, h.upstream.Scheme) || !strings.EqualFold(parsed.Host, h.upstream.Host) {
		return
	}

	parsed.Scheme = ""
	parsed.Host = ""
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}
	header.Set("Location", parsed.String())
}

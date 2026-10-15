// Package ipfs is a minimal client for the embedded Kubo node's HTTP API.
package ipfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultIPFSAPIURL = "http://127.0.0.1:5001"
	maxAPIResponse    = 1 << 20
)

type IPFSStats struct {
	NumObjects uint64 `json:"NumObjects"`
	SizeStat   struct {
		RepoSize   uint64 `json:"RepoSize"`
		StorageMax uint64 `json:"StorageMax"`
	} `json:"SizeStat"`
}

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func NewClient(rawURL string) (*Client, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = defaultIPFSAPIURL
	}

	baseURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse IPFS API URL: %w", err)
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return nil, fmt.Errorf("IPFS API URL must be an http(s) URL")
	}

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       60 * time.Second,
			},
		},
	}, nil
}

func (c *Client) endpoint(endpointPath string) string {
	endpointURL := *c.baseURL
	basePath := strings.Trim(endpointURL.Path, "/")
	endpointURL.Path = "/" + path.Join(basePath, endpointPath)
	return endpointURL.String()
}

func (c *Client) RepoStats(ctx context.Context) (IPFSStats, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint("api/v0/stats/repo"),
		http.NoBody,
	)
	if err != nil {
		return IPFSStats{}, fmt.Errorf("create IPFS stats request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return IPFSStats{}, fmt.Errorf("request IPFS stats: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return IPFSStats{}, fmt.Errorf("read IPFS stats response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return IPFSStats{}, fmt.Errorf("IPFS API returned %s: %s", resp.Status, message)
	}

	return decodeRepoStats(body)
}

// repoStatWire is what Kubo's /api/v0/stats/repo actually returns:
// RepoSize and StorageMax are top-level fields. The nested SizeStat form
// is accepted too so older or proxied responses keep working.
type repoStatWire struct {
	NumObjects uint64  `json:"NumObjects"`
	RepoSize   *uint64 `json:"RepoSize"`
	StorageMax *uint64 `json:"StorageMax"`
	SizeStat   *struct {
		RepoSize   uint64 `json:"RepoSize"`
		StorageMax uint64 `json:"StorageMax"`
	} `json:"SizeStat"`
}

func decodeRepoStats(body []byte) (IPFSStats, error) {
	var wire repoStatWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return IPFSStats{}, fmt.Errorf("decode IPFS stats response: %w", err)
	}
	var stats IPFSStats
	stats.NumObjects = wire.NumObjects
	if wire.SizeStat != nil {
		stats.SizeStat.RepoSize = wire.SizeStat.RepoSize
		stats.SizeStat.StorageMax = wire.SizeStat.StorageMax
	}
	if wire.RepoSize != nil {
		stats.SizeStat.RepoSize = *wire.RepoSize
	}
	if wire.StorageMax != nil {
		stats.SizeStat.StorageMax = *wire.StorageMax
	}
	return stats, nil
}

// StatusError is returned when the Kubo API answers with an error status.
type StatusError struct {
	status  int
	message string
}

func (e *StatusError) Error() string {
	return e.message
}

func (e *StatusError) StatusCode() int {
	return e.status
}

func (c *Client) Cat(ctx context.Context, cid string) (*http.Response, error) {
	endpointURL, err := url.Parse(c.endpoint("api/v0/cat"))
	if err != nil {
		return nil, fmt.Errorf("parse IPFS cat URL: %w", err)
	}
	query := endpointURL.Query()
	query.Set("arg", cid)
	query.Set("offline", "true")
	query.Set("progress", "false")
	endpointURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create IPFS cat request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request IPFS cat: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, &StatusError{
			status:  resp.StatusCode,
			message: fmt.Sprintf("IPFS cat returned %s: %s", resp.Status, message),
		}
	}
	return resp, nil
}

type UploadFile struct {
	Name      string
	TempPath  string
	Size      int64
	Directory bool
}

type AddResult struct {
	CID  string
	Size int64
	Name string
}

type addEntry struct {
	Name string          `json:"Name"`
	Hash string          `json:"Hash"`
	Size json.RawMessage `json:"Size"`
}

func (c *Client) Add(ctx context.Context, files []UploadFile, folder bool) (AddResult, error) {
	endpointURL, err := url.Parse(c.endpoint("api/v0/add"))
	if err != nil {
		return AddResult{}, fmt.Errorf("parse IPFS add URL: %w", err)
	}
	query := endpointURL.Query()
	query.Set("pin", "false")
	query.Set("progress", "false")
	if folder {
		query.Set("empty-dirs", "true")
		query.Set("recursive", "true")
		query.Set("wrap-with-directory", "true")
	}
	endpointURL.RawQuery = query.Encode()

	pipeReader, pipeWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(pipeWriter)
	writeDone := make(chan error, 1)
	go func() {
		writeErr := writeAddMultipart(multipartWriter, files)
		_ = pipeWriter.CloseWithError(writeErr)
		writeDone <- writeErr
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), pipeReader)
	if err != nil {
		_ = pipeReader.CloseWithError(err)
		<-writeDone
		return AddResult{}, fmt.Errorf("create IPFS add request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		_ = pipeReader.CloseWithError(err)
		<-writeDone
		return AddResult{}, fmt.Errorf("request IPFS add: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		<-writeDone
		return AddResult{}, fmt.Errorf("IPFS add returned %s: %s", resp.Status, message)
	}

	result, readErr := readAddResponse(resp.Body)
	writeErr := <-writeDone
	if readErr != nil {
		return AddResult{}, readErr
	}
	if writeErr != nil {
		return AddResult{}, fmt.Errorf("send upload to IPFS: %w", writeErr)
	}
	return result, nil
}

func writeAddMultipart(writer *multipart.Writer, files []UploadFile) error {
	for _, file := range files {
		if file.Directory {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf(
				`form-data; name="file"; filename="%s"`,
				escapeMultipartFilename(file.Name),
			))
			header.Set("Content-Type", "application/x-directory")
			if _, err := writer.CreatePart(header); err != nil {
				return fmt.Errorf("create directory multipart part: %w", err)
			}
			continue
		}

		part, err := writer.CreateFormFile("file", file.Name)
		if err != nil {
			return fmt.Errorf("create file multipart part: %w", err)
		}
		source, err := os.Open(file.TempPath)
		if err != nil {
			return fmt.Errorf("open upload temporary file: %w", err)
		}
		_, copyErr := io.Copy(part, source)
		closeErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("stream upload temporary file: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close upload temporary file: %w", closeErr)
		}
	}
	return writer.Close()
}

func escapeMultipartFilename(name string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\r", "",
		"\n", "",
	).Replace(name)
}

func readAddResponse(reader io.Reader) (AddResult, error) {
	decoder := json.NewDecoder(reader)
	var last addEntry
	found := false
	for {
		var entry addEntry
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return AddResult{}, fmt.Errorf("decode IPFS add response: %w", err)
		}
		if entry.Hash != "" {
			last = entry
			found = true
		}
	}
	if !found {
		return AddResult{}, fmt.Errorf("IPFS add response did not contain a CID")
	}

	size, err := parseAddSize(last.Size)
	if err != nil {
		return AddResult{}, fmt.Errorf("parse IPFS add size: %w", err)
	}
	return AddResult{CID: last.Hash, Size: size, Name: last.Name}, nil
}

func parseAddSize(raw json.RawMessage) (int64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, nil
	}
	if strings.HasPrefix(value, `"`) {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
		return strconv.ParseInt(text, 10, 64)
	}
	return strconv.ParseInt(value, 10, 64)
}

func (c *Client) postJSON(ctx context.Context, endpointPath string, query url.Values, target any) error {
	endpointURL, err := url.Parse(c.endpoint(endpointPath))
	if err != nil {
		return fmt.Errorf("parse IPFS %s URL: %w", endpointPath, err)
	}
	endpointURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("create IPFS %s request: %w", endpointPath, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request IPFS %s: %w", endpointPath, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return fmt.Errorf("read IPFS %s response: %w", endpointPath, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return &StatusError{
			status:  resp.StatusCode,
			message: fmt.Sprintf("IPFS %s returned %s: %s", endpointPath, resp.Status, message),
		}
	}
	if target != nil {
		if err := json.Unmarshal(body, target); err != nil {
			return fmt.Errorf("decode IPFS %s response: %w", endpointPath, err)
		}
	}
	return nil
}

type BlockStat struct {
	Key  string `json:"Key"`
	Size int64  `json:"Size"`
}

// BlockStat reports the local block for a CID. The lookup is offline: without
// it, Kubo tries to fetch a missing block from the network, which stalls a
// local-only availability check until it times out.
func (c *Client) BlockStat(ctx context.Context, cid string) (BlockStat, error) {
	var stat BlockStat
	if err := c.postJSON(ctx, "api/v0/block/stat", offlineValues(cid), &stat); err != nil {
		return BlockStat{}, err
	}
	return stat, nil
}

type ObjectStat struct {
	Hash           string `json:"Hash"`
	NumLinks       int64  `json:"NumLinks"`
	BlockSize      int64  `json:"BlockSize"`
	LinksSize      int64  `json:"LinksSize"`
	DataSize       int64  `json:"DataSize"`
	CumulativeSize int64  `json:"CumulativeSize"`
	// Type is "file" or "directory" as reported by files/stat. It is empty on
	// nodes that only serve the older object/stat.
	Type string `json:"Type,omitempty"`
	// Blocks is the number of blocks a UnixFS file is stored in.
	Blocks int64 `json:"Blocks,omitempty"`
}

// IsDirectory reports whether the stat describes a directory. Older nodes
// answer with NumLinks only; current ones answer with Type.
func (s ObjectStat) IsDirectory() bool {
	return s.NumLinks > 0 || strings.EqualFold(s.Type, "directory")
}

// ContentSize is the number of bytes of content the root holds: DataSize for a
// small file, CumulativeSize when it is the only meaningful figure.
func (s ObjectStat) ContentSize() int64 {
	if s.CumulativeSize > 0 {
		return s.CumulativeSize
	}
	return s.DataSize
}

// offlineValues builds the argument for a Kubo lookup that must consult only
// the local repository.
func offlineValues(cid string) url.Values {
	return url.Values{"arg": {cid}, "offline": {"true"}}
}

// ObjectStat describes the UnixFS root behind a CID.
//
// Kubo removed object/stat in favour of files/stat, so files/stat is asked
// first and object/stat is kept as a fallback for older nodes. Both are called
// offline: this describes a CID in *our* repository, so reaching for the
// network would be both slow and misleading.
func (c *Client) ObjectStat(ctx context.Context, cid string) (ObjectStat, error) {
	stat, filesErr := c.filesStat(ctx, cid)
	if filesErr == nil {
		return stat, nil
	}
	var legacy ObjectStat
	if err := c.postJSON(ctx, "api/v0/object/stat", offlineValues(cid), &legacy); err != nil {
		// Neither endpoint worked. Prefer the files/stat failure: it is the
		// one that describes a current node.
		return ObjectStat{}, filesErr
	}
	return legacy, nil
}

type filesStat struct {
	Hash           string `json:"Hash"`
	Size           int64  `json:"Size"`
	CumulativeSize int64  `json:"CumulativeSize"`
	Blocks         int64  `json:"Blocks"`
	Type           string `json:"Type"`
}

func (c *Client) filesStat(ctx context.Context, cid string) (ObjectStat, error) {
	// files/stat takes a path rather than a bare CID.
	var stat filesStat
	values := url.Values{"arg": {"/ipfs/" + cid}, "offline": {"true"}}
	if err := c.postJSON(ctx, "api/v0/files/stat", values, &stat); err != nil {
		return ObjectStat{}, err
	}
	return ObjectStat{
		Hash:           stat.Hash,
		DataSize:       stat.Size,
		CumulativeSize: stat.CumulativeSize,
		Blocks:         stat.Blocks,
		Type:           stat.Type,
	}, nil
}

type LsLink struct {
	Name   string `json:"Name"`
	Hash   string `json:"Hash,omitempty"`
	Size   uint64 `json:"Size"`
	Type   int    `json:"Type"`
	Target string `json:"Target,omitempty"`
}

type lsObject struct {
	Hash  string   `json:"Hash"`
	Links []LsLink `json:"Links"`
}

type lsResponse struct {
	Objects []lsObject `json:"Objects"`
}

// Ls lists the entries of a directory. Like every other lookup it stays local,
// so a CID that is not in this repository fails fast instead of hanging on a
// network fetch.
func (c *Client) Ls(ctx context.Context, cid string) ([]LsLink, error) {
	var output lsResponse
	if err := c.postJSON(ctx, "api/v0/ls", offlineValues(cid), &output); err != nil {
		return nil, err
	}
	if len(output.Objects) > 0 {
		return output.Objects[0].Links, nil
	}
	return nil, nil
}

func (c *Client) Content(ctx context.Context, cid string) ([]byte, json.RawMessage, error) {
	resp, err := c.Cat(ctx, cid)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return nil, nil, err
	}
	var decoded json.RawMessage
	if err := json.Unmarshal(data, &decoded); err == nil {
		return data, decoded, nil
	}
	return data, nil, nil
}

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
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
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

	var stats IPFSStats
	if err := json.Unmarshal(body, &stats); err != nil {
		return IPFSStats{}, fmt.Errorf("decode IPFS stats response: %w", err)
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

func (c *Client) BlockStat(ctx context.Context, cid string) (BlockStat, error) {
	var stat BlockStat
	if err := c.postJSON(ctx, "api/v0/block/stat", url.Values{"arg": {cid}}, &stat); err != nil {
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
}

func (c *Client) ObjectStat(ctx context.Context, cid string) (ObjectStat, error) {
	var stat ObjectStat
	if err := c.postJSON(ctx, "api/v0/object/stat", url.Values{"arg": {cid}}, &stat); err != nil {
		return ObjectStat{}, err
	}
	return stat, nil
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

func (c *Client) Ls(ctx context.Context, cid string) ([]LsLink, error) {
	var output lsResponse
	if err := c.postJSON(ctx, "api/v0/ls", url.Values{"arg": {cid}}, &output); err != nil {
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

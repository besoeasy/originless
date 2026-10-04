package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/besoeasy/originless/internal/ipfs"
)

const (
	maxUploadBytes       int64 = 1 << 30
	maxMultipartOverhead int64 = 1 << 20
)

var (
	errNoUploadFiles  = errors.New("no files provided")
	errUploadTooLarge = errors.New("upload exceeds maximum size")
)

type uploadResponse struct {
	CID       string `json:"cid"`
	Size      int64  `json:"size"`
	Bytes     int64  `json:"bytes"`
	Name      string `json:"name,omitempty"`
	Extension string `json:"extension"`
	MIME      string `json:"mime"`
	Files     int    `json:"files,omitempty"`
}

type uploadHandler struct {
	client *ipfs.Client
	folder bool
}

func (h *uploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	expectedPath := "/up"
	if h.folder {
		expectedPath = "/upf"
	}
	if r.URL.Path != expectedPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed",
		})
		return
	}

	// Multipart parts are spooled to disk before they are added to IPFS, so
	// check there is room for the whole request before reading any of it.
	spool, err := h.spoolDir()
	if err != nil {
		log.Printf("upload spool directory unavailable: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "upload storage is unavailable",
		})
		return
	}
	if err := ensureFreeSpace(spool, maxUploadBytes); err != nil {
		log.Printf("upload rejected, insufficient spool space: %v", err)
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusInsufficientStorage, map[string]string{
			"error": "not enough free space to accept this upload",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+maxMultipartOverhead)
	files, totalBytes, err := parseUploadFiles(r, maxUploadBytes, spool)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errUploadTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	defer removeUploadFiles(files)

	folder := h.folder || uploadIsFolder(files)
	result, err := h.client.Add(r.Context(), files, folder)
	if err != nil {
		log.Printf("IPFS add failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "IPFS add failed",
		})
		return
	}

	name := result.Name
	if name == "" && len(files) > 0 {
		name = files[0].Name
	}
	extension, mimeType := uploadMetadata(name, folder)
	size := result.Size
	if size == 0 {
		size = totalBytes
	}
	writeJSON(w, http.StatusOK, uploadResponse{
		CID:       result.CID,
		Size:      size,
		Bytes:     totalBytes,
		Name:      name,
		Extension: extension,
		MIME:      mimeType,
		Files:     countUploadFiles(files),
	})
}

// spoolDir returns the directory multipart parts are written to: the system
// temporary directory, which the container's filesystem always provides.
func (h *uploadHandler) spoolDir() (string, error) {
	return os.TempDir(), nil
}

func parseUploadFiles(r *http.Request, maxBytes int64, spoolDir string) (files []ipfs.UploadFile, totalBytes int64, err error) {
	defer func() {
		if err != nil {
			removeUploadFiles(files)
			files = nil
		}
	}()

	reader, err := r.MultipartReader()
	if err != nil {
		return nil, 0, fmt.Errorf("request must be multipart/form-data: %w", err)
	}

	seen := make(map[string]struct{})
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, totalBytes, fmt.Errorf("read multipart upload: %w", nextErr)
		}

		name, nameErr := multipartPartFilename(part)
		if nameErr != nil {
			_ = part.Close()
			return nil, totalBytes, nameErr
		}
		if name == "" {
			_, copyErr := io.Copy(io.Discard, part)
			_ = part.Close()
			if copyErr != nil {
				return nil, totalBytes, fmt.Errorf("read multipart field: %w", copyErr)
			}
			continue
		}
		if _, exists := seen[name]; exists {
			_ = part.Close()
			return nil, totalBytes, fmt.Errorf("duplicate upload path %q", name)
		}
		seen[name] = struct{}{}

		contentType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		isDirectory := strings.EqualFold(contentType, "application/x-directory")
		if isDirectory {
			written, copyErr := copyUploadPart(io.Discard, part, maxBytes-totalBytes)
			_ = part.Close()
			if copyErr != nil {
				return nil, totalBytes, copyErr
			}
			totalBytes += written
			files = append(files, ipfs.UploadFile{Name: name, Directory: true})
			continue
		}

		temp, createErr := os.CreateTemp(spoolDir, "originless-upload-*")
		if createErr != nil {
			_ = part.Close()
			return nil, totalBytes, fmt.Errorf("create upload temporary file: %w", createErr)
		}
		tempPath := temp.Name()
		written, copyErr := copyUploadPart(temp, part, maxBytes-totalBytes)
		closeErr := temp.Close()
		_ = part.Close()
		if copyErr != nil {
			_ = os.Remove(tempPath)
			return nil, totalBytes, copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tempPath)
			return nil, totalBytes, fmt.Errorf("close upload temporary file: %w", closeErr)
		}
		totalBytes += written
		files = append(files, ipfs.UploadFile{
			Name:     name,
			TempPath: tempPath,
			Size:     written,
		})
	}

	if len(files) == 0 {
		return nil, totalBytes, errNoUploadFiles
	}
	return files, totalBytes, nil
}

func copyUploadPart(dst io.Writer, src io.Reader, remaining int64) (int64, error) {
	if remaining < 0 {
		return 0, errUploadTooLarge
	}
	written, err := io.Copy(dst, io.LimitReader(src, remaining+1))
	if err != nil {
		return written, fmt.Errorf("copy upload data: %w", err)
	}
	if written > remaining {
		return written, errUploadTooLarge
	}
	return written, nil
}

func multipartPartFilename(part *multipart.Part) (string, error) {
	disposition := part.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return "", fmt.Errorf("parse multipart Content-Disposition: %w", err)
	}

	name := params["filename"]
	if headerName := strings.TrimSpace(part.Header.Get("X-File-Path")); headerName != "" {
		name = headerName
	}
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "", nil
	}
	if strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("invalid upload path")
	}
	if strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':') {
		return "", fmt.Errorf("upload path must be relative")
	}

	name = path.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("upload path escapes the upload root")
	}
	return name, nil
}

func removeUploadFiles(files []ipfs.UploadFile) {
	for _, file := range files {
		if file.TempPath != "" {
			_ = os.Remove(file.TempPath)
		}
	}
}

func countUploadFiles(files []ipfs.UploadFile) int {
	count := 0
	for _, file := range files {
		if !file.Directory {
			count++
		}
	}
	return count
}

func uploadIsFolder(files []ipfs.UploadFile) bool {
	if len(files) > 1 {
		return true
	}
	for _, file := range files {
		if file.Directory || strings.Contains(file.Name, "/") {
			return true
		}
	}
	return false
}

func uploadMetadata(name string, folder bool) (string, string) {
	if folder {
		return "", "inode/directory"
	}
	if strings.TrimSpace(name) == "" {
		return "", "application/octet-stream"
	}

	baseName := path.Base(strings.ReplaceAll(name, "\\", "/"))
	extension := strings.ToLower(path.Ext(baseName))
	if extension == "" {
		return "", "application/octet-stream"
	}

	switch extension {
	case ".jpg", ".jpeg":
		return extension, "image/jpeg"
	case ".opus":
		return extension, "audio/opus"
	case ".bin":
		return extension, "application/octet-stream"
	}

	mimeType := mime.TypeByExtension(extension)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return extension, mimeType
}

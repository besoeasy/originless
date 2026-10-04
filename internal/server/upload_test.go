package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/besoeasy/originless/internal/ipfs"
)

type testUploadPart struct {
	fieldName string
	fileName  string
	content   string
}

func newUploadRequest(t *testing.T, path string, parts ...testUploadPart) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		multipartPart, err := writer.CreateFormFile(part.fieldName, part.fileName)
		if err != nil {
			t.Fatalf("create multipart part: %v", err)
		}
		if _, err := multipartPart.Write([]byte(part.content)); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func newAddResponseServer(t *testing.T, response string, inspect func(*http.Request, []string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v0/add" {
			http.NotFound(w, r)
			return
		}

		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var names []string
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				http.Error(w, nextErr.Error(), http.StatusBadRequest)
				return
			}
			_, params, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if parseErr != nil {
				http.Error(w, parseErr.Error(), http.StatusBadRequest)
				return
			}
			names = append(names, params["filename"])
			if _, readErr := io.Copy(io.Discard, part); readErr != nil {
				http.Error(w, readErr.Error(), http.StatusBadRequest)
				return
			}
			_ = part.Close()
		}
		if inspect != nil {
			inspect(r, names)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
}

func decodeUploadResponse(t *testing.T, recorder *httptest.ResponseRecorder) uploadResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response uploadResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return response
}

func TestUpUploadsSingleFileWithoutPinning(t *testing.T) {
	var pin string
	var wrap string
	var names []string
	server := newAddResponseServer(t,
		"{\"Name\":\"hello.txt\",\"Hash\":\"bafy-file\",\"Size\":\"5\"}\n",
		func(r *http.Request, receivedNames []string) {
			pin = r.URL.Query().Get("pin")
			wrap = r.URL.Query().Get("wrap-with-directory")
			names = receivedNames
		},
	)
	defer server.Close()

	client, err := ipfs.NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := newUploadRequest(t, "/up", testUploadPart{
		fieldName: "file",
		fileName:  "hello.txt",
		content:   "hello",
	})
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, request)
	response := decodeUploadResponse(t, recorder)

	if pin != "false" {
		t.Errorf("pin = %q, want false", pin)
	}
	if wrap != "" {
		t.Errorf("wrap-with-directory = %q, want unset for a single file", wrap)
	}
	if len(names) != 1 || names[0] != "hello.txt" {
		t.Errorf("upstream filenames = %v, want [hello.txt]", names)
	}
	if response.CID != "bafy-file" || response.Size != 5 || response.Bytes != 5 {
		t.Errorf("response = %+v, want bafy-file with size 5", response)
	}
	if response.Extension != ".txt" || response.MIME == "" {
		t.Errorf("metadata = %q/%q, want .txt and a MIME type", response.Extension, response.MIME)
	}
}

func TestUpAutomaticallyHandlesFolders(t *testing.T) {
	var recursive string
	var wrap string
	var names []string
	server := newAddResponseServer(t,
		"{\"Name\":\"folder/one.txt\",\"Hash\":\"bafy-one\",\"Size\":\"3\"}\n"+
			"{\"Name\":\"folder/two.txt\",\"Hash\":\"bafy-two\",\"Size\":\"3\"}\n"+
			"{\"Name\":\"folder\",\"Hash\":\"bafy-root\",\"Size\":\"42\"}\n",
		func(r *http.Request, receivedNames []string) {
			recursive = r.URL.Query().Get("recursive")
			wrap = r.URL.Query().Get("wrap-with-directory")
			names = receivedNames
		},
	)
	defer server.Close()

	client, err := ipfs.NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := newUploadRequest(t, "/up",
		testUploadPart{fieldName: "file", fileName: "folder/one.txt", content: "one"},
		testUploadPart{fieldName: "file", fileName: "folder/two.txt", content: "two"},
	)
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, request)
	response := decodeUploadResponse(t, recorder)

	if recursive != "true" || wrap != "true" {
		t.Errorf("folder options recursive=%q wrap=%q, want true/true", recursive, wrap)
	}
	if len(names) != 2 || names[0] != "folder/one.txt" || names[1] != "folder/two.txt" {
		t.Errorf("upstream filenames = %v, want folder paths", names)
	}
	if response.CID != "bafy-root" || response.Size != 42 || response.Bytes != 6 || response.Files != 2 {
		t.Errorf("response = %+v, want folder root response", response)
	}
	if response.Extension != "" || response.MIME != "inode/directory" {
		t.Errorf("folder metadata = %q/%q, want empty extension and inode/directory", response.Extension, response.MIME)
	}
}

func TestUpfForcesFolderMode(t *testing.T) {
	var wrap string
	server := newAddResponseServer(t,
		"{\"Name\":\"folder\",\"Hash\":\"bafy-root\",\"Size\":\"10\"}\n",
		func(r *http.Request, _ []string) {
			wrap = r.URL.Query().Get("wrap-with-directory")
		},
	)
	defer server.Close()

	client, err := ipfs.NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := newUploadRequest(t, "/upf", testUploadPart{
		fieldName: "file",
		fileName:  "one.txt",
		content:   "one",
	})
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, request)
	response := decodeUploadResponse(t, recorder)

	if wrap != "true" {
		t.Errorf("wrap-with-directory = %q, want true", wrap)
	}
	if response.CID != "bafy-root" || response.Files != 1 {
		t.Errorf("response = %+v, want folder response", response)
	}
}

func TestUploadMetadata(t *testing.T) {
	tests := []struct {
		name      string
		folder    bool
		extension string
		mimeType  string
	}{
		{name: "photo.JPG", extension: ".jpg", mimeType: "image/jpeg"},
		{name: "track.opus", extension: ".opus", mimeType: "audio/opus"},
		{name: "payload.BIN", extension: ".bin", mimeType: "application/octet-stream"},
		{name: "README", extension: "", mimeType: "application/octet-stream"},
		{name: "folder", folder: true, extension: "", mimeType: "inode/directory"},
	}

	for _, test := range tests {
		extension, mimeType := uploadMetadata(test.name, test.folder)
		if extension != test.extension || mimeType != test.mimeType {
			t.Errorf("uploadMetadata(%q, folder=%t) = %q/%q, want %q/%q", test.name, test.folder, extension, mimeType, test.extension, test.mimeType)
		}
	}
}

func TestUploadRejectsPathTraversal(t *testing.T) {
	client, err := ipfs.NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := newUploadRequest(t, "/up", testUploadPart{
		fieldName: "file",
		fileName:  "../secret.txt",
		content:   "secret",
	})
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "upload path") {
		t.Errorf("response body = %q, want path validation error", recorder.Body.String())
	}
}

func TestUploadIPFSFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "add failed", http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := ipfs.NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := newUploadRequest(t, "/up", testUploadPart{
		fieldName: "file",
		fileName:  "hello.txt",
		content:   "hello",
	})
	recorder := httptest.NewRecorder()
	mustNewRouter(t, client).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if !strings.Contains(recorder.Body.String(), "IPFS add failed") {
		t.Errorf("response body = %q, want upstream error", recorder.Body.String())
	}
}

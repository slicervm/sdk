package slicer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCpToVMChunkedUploadsManifestAndOrderedChunks(t *testing.T) {
	var mu sync.Mutex
	uploads := map[string][]byte{}
	var execCalls [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			mu.Lock()
			execCalls = append(execCalls, append([]string(nil), r.URL.Query()["args"]...))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"exit_code":0}`)
		case strings.HasSuffix(r.URL.Path, "/cp"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			uploads[r.URL.Query().Get("path")] = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := filepath.Join(t.TempDir(), "tool")
	contents := []byte("abcdefghijkl")
	if err := os.WriteFile(source, contents, 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	client := NewSlicerClient(server.URL, "token", "test", server.Client())
	err := client.CpToVMChunked(context.Background(), "vm-1", source, "/home/ubuntu/bin/tool", ChunkedCopyOptions{
		UID:         1000,
		GID:         1000,
		Permissions: "0755",
		Mode:        "binary",
		ChunkSize:   4,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("CpToVMChunked: %v", err)
	}

	var manifest CopyManifest
	var manifestPath string
	for uploadPath, body := range uploads {
		if path.Base(uploadPath) == "manifest.json" {
			manifestPath = uploadPath
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
		}
	}
	if manifestPath == "" {
		t.Fatal("manifest was not uploaded")
	}
	if manifest.Destination != "/home/ubuntu/bin/tool" || manifest.Size != int64(len(contents)) || len(manifest.Chunks) != 3 {
		t.Fatalf("manifest = %+v", manifest)
	}
	var assembled []byte
	for _, chunk := range manifest.Chunks {
		chunkPath := path.Join(path.Dir(manifestPath), "chunks", CopyChunkFileName(chunk))
		assembled = append(assembled, uploads[chunkPath]...)
	}
	if string(assembled) != string(contents) {
		t.Fatalf("assembled chunks = %q, want %q", assembled, contents)
	}
	if len(execCalls) != 2 || strings.Join(execCalls[0], " ") != "upload check" || !strings.HasPrefix(strings.Join(execCalls[1], " "), "upload finalise ") {
		t.Fatalf("exec calls = %#v", execCalls)
	}
}

func TestCopySessionPathUsesDestinationParent(t *testing.T) {
	got, err := copySessionPath("/home/ubuntu/.arkade/bin", strings.Repeat("a", 32))
	if err != nil {
		t.Fatalf("copySessionPath: %v", err)
	}
	want := "/home/ubuntu/.arkade/.slicer-upload-" + strings.Repeat("a", 32)
	if got != want {
		t.Fatalf("copySessionPath() = %q, want %q", got, want)
	}
}

func TestCpToVMChunkedAbortsFailedUpload(t *testing.T) {
	var mu sync.Mutex
	var execCalls [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			mu.Lock()
			execCalls = append(execCalls, append([]string(nil), r.URL.Query()["args"]...))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"exit_code":0}`)
		case strings.HasSuffix(r.URL.Path, "/cp"):
			http.Error(w, "request is too large", http.StatusRequestEntityTooLarge)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	client := NewSlicerClient(server.URL, "token", "test", server.Client())
	err := client.CpToVMChunked(context.Background(), "vm-1", source, "/home/ubuntu/tool", ChunkedCopyOptions{
		Mode:      "binary",
		ChunkSize: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "413 Request Entity Too Large") {
		t.Fatalf("error = %v, want request-size failure", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(execCalls) != 2 || strings.Join(execCalls[0], " ") != "upload check" || !strings.HasPrefix(strings.Join(execCalls[1], " "), "upload abort ") {
		t.Fatalf("exec calls = %#v", execCalls)
	}
}

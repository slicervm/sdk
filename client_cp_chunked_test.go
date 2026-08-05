package slicer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"time"
)

func TestCpToVMChunkedUploadsManifestAndOrderedChunks(t *testing.T) {
	var mu sync.Mutex
	uploads := map[string][]byte{}
	contentLengths := map[string]int64{}
	var execCalls [][]string
	activeChunks := 0
	maxActiveChunks := 0
	completedChunks := 0
	manifestArrivedEarly := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			mu.Lock()
			execCalls = append(execCalls, append([]string(nil), r.URL.Query()["args"]...))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"exit_code":0,"stdout":"chunked-copy-v2\n"}`)
		case strings.HasSuffix(r.URL.Path, "/cp"):
			uploadPath := r.URL.Query().Get("path")
			isManifest := path.Base(uploadPath) == "manifest.json"
			if !isManifest {
				mu.Lock()
				activeChunks++
				if activeChunks > maxActiveChunks {
					maxActiveChunks = activeChunks
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			uploads[uploadPath] = body
			contentLengths[uploadPath] = r.ContentLength
			if isManifest {
				manifestArrivedEarly = completedChunks != 3
			} else {
				activeChunks--
				completedChunks++
			}
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
	if manifest.UID != 1000 || manifest.GID != 1000 {
		t.Fatalf("manifest ownership = %d:%d, want 1000:1000", manifest.UID, manifest.GID)
	}
	if manifest.Version != ChunkedCopyManifestV2 || manifest.CopySemantics != cpCopySemanticsV1 ||
		manifest.SourceName != "tool" || manifest.SourceType != copySourceTypeFile || manifest.CopyContents {
		t.Fatalf("copy semantics manifest = %+v", manifest)
	}
	var assembled []byte
	for _, chunk := range manifest.Chunks {
		chunkPath := path.Join(path.Dir(manifestPath), "chunks", CopyChunkFileName(chunk))
		chunkData := uploads[chunkPath]
		assembled = append(assembled, chunkData...)
		if contentLengths[chunkPath] != chunk.Size {
			t.Fatalf("Content-Length for chunk %d = %d, want %d", chunk.Index, contentLengths[chunkPath], chunk.Size)
		}
		sum := sha256.Sum256(chunkData)
		if got := hex.EncodeToString(sum[:]); got != chunk.SHA256 {
			t.Fatalf("chunk %d SHA-256 = %s, want %s", chunk.Index, got, chunk.SHA256)
		}
	}
	if string(assembled) != string(contents) {
		t.Fatalf("assembled chunks = %q, want %q", assembled, contents)
	}
	if len(execCalls) != 2 || strings.Join(execCalls[0], " ") != "upload check" || !strings.HasPrefix(strings.Join(execCalls[1], " "), "upload finalise ") {
		t.Fatalf("exec calls = %#v", execCalls)
	}
	if maxActiveChunks < 2 {
		t.Fatalf("maximum concurrent chunks = %d, want at least 2", maxActiveChunks)
	}
	if manifestArrivedEarly {
		t.Fatal("manifest arrived before every chunk completed")
	}
}

func TestPrepareChunkedTarStagesOutsideSourceAndCleansUp(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("contents"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	prepared, err := prepareChunkedCopySource(context.Background(), source, ChunkedCopyOptions{Mode: "tar"})
	if err != nil {
		t.Fatalf("prepareChunkedCopySource: %v", err)
	}
	stagedPath := prepared.file.Name()
	if strings.HasPrefix(stagedPath, root+string(filepath.Separator)) {
		t.Fatalf("staged tar %s is inside source tree %s", stagedPath, root)
	}
	if prepared.size == 0 || prepared.unpackedSize == 0 {
		t.Fatalf("staged sizes = %d, %d", prepared.size, prepared.unpackedSize)
	}
	if err := prepared.cleanup(); err != nil {
		t.Fatalf("cleanup staged tar: %v", err)
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("staged tar still exists: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUploadCopyBytesHandlesNilErrorBody(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusRequestEntityTooLarge,
			Status:     "413 Request Entity Too Large",
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	client := NewSlicerClient("http://slicer.invalid", "token", "test", httpClient)
	err := client.uploadCopyBytes(context.Background(), "vm-1", "/home/ubuntu/chunk", 1000, 1000, []byte("data"))
	if err == nil || !strings.Contains(err.Error(), "413 Request Entity Too Large") {
		t.Fatalf("error = %v", err)
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

func TestCpToVMChunkedCancellationAbortsWithoutManifest(t *testing.T) {
	var mu sync.Mutex
	var execCalls [][]string
	manifestUploaded := false
	chunkStarted := make(chan struct{}, 1)
	releaseChunks := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			mu.Lock()
			execCalls = append(execCalls, append([]string(nil), r.URL.Query()["args"]...))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"exit_code":0}`)
		case strings.HasSuffix(r.URL.Path, "/cp"):
			if path.Base(r.URL.Query().Get("path")) == "manifest.json" {
				mu.Lock()
				manifestUploaded = true
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
				return
			}
			select {
			case chunkStarted <- struct{}{}:
			default:
			}
			<-releaseChunks
			http.Error(w, "cancelled", http.StatusRequestTimeout)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(source, []byte("abcdefghijkl"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := NewSlicerClient(server.URL, "token", "test", server.Client())
	done := make(chan error, 1)
	go func() {
		done <- client.CpToVMChunked(ctx, "vm-1", source, "/home/ubuntu/tool", ChunkedCopyOptions{
			Mode:        "binary",
			ChunkSize:   4,
			Concurrency: 2,
		})
	}()
	select {
	case <-chunkStarted:
		cancel()
		close(releaseChunks)
	case <-time.After(2 * time.Second):
		t.Fatal("chunk request did not start")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled copy succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled copy did not return promptly")
	}

	mu.Lock()
	defer mu.Unlock()
	if manifestUploaded {
		t.Fatal("manifest was uploaded after cancellation")
	}
	if len(execCalls) != 2 || strings.Join(execCalls[0], " ") != "upload check" || !strings.HasPrefix(strings.Join(execCalls[1], " "), "upload abort ") {
		t.Fatalf("exec calls = %#v", execCalls)
	}
}

func TestCpToVMChunkedCleansStagedTarAfterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exec") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"exit_code":0}`)
			return
		}
		http.Error(w, "failed", http.StatusBadGateway)
	}))
	defer server.Close()

	root := t.TempDir()
	source := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("contents"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	client := NewSlicerClient(server.URL, "token", "test", server.Client())
	err := client.CpToVMChunked(context.Background(), "vm-1", source, "/home/ubuntu/workspace", ChunkedCopyOptions{
		Mode:      "tar",
		ChunkSize: 512,
	})
	if err == nil {
		t.Fatal("tar copy succeeded")
	}
	staged, globErr := filepath.Glob(filepath.Join(root, ".slicer-upload-*.tar"))
	if globErr != nil {
		t.Fatalf("glob staged tar: %v", globErr)
	}
	if len(staged) != 0 {
		t.Fatalf("staged tar remains: %v", staged)
	}
}

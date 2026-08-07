package slicer

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCpToVMSendsCopySemanticsInSingleRequest(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		directory    bool
		contentsOnly bool
		destination  string
	}{
		{name: "binary", mode: "binary", destination: "/tmp/dest/"},
		{name: "tar contents", mode: "tar", directory: true, contentsOnly: true, destination: "/tmp/dest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if test.directory {
				if err := os.Mkdir(source, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			localPath := source
			if test.contentsOnly {
				localPath += string(filepath.Separator) + "."
			}

			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				_, _ = io.Copy(io.Discard, r.Body)
				q := r.URL.Query()
				if got := q.Get("copy_semantics"); got != cpCopySemanticsV1 {
					t.Errorf("copy_semantics = %q", got)
				}
				wantMode := cpWireModeBinaryV1
				if test.directory {
					wantMode = cpWireModeTarV1
				}
				if got := q.Get("mode"); got != wantMode {
					t.Errorf("mode = %q, want %q", got, wantMode)
				}
				if got := q.Get("source_name"); got != "source" {
					t.Errorf("source_name = %q", got)
				}
				wantType := copySourceTypeFile
				if test.directory {
					wantType = copySourceTypeDir
				}
				if got := q.Get("source_type"); got != wantType {
					t.Errorf("source_type = %q, want %q", got, wantType)
				}
				if got := q.Get("copy_contents"); got != map[bool]string{true: "true", false: ""}[test.contentsOnly] {
					t.Errorf("copy_contents = %q", got)
				}
				if got := q.Get("path"); got != test.destination {
					t.Errorf("path = %q, want %q", got, test.destination)
				}
				if got := q.Get("uid"); got != "" {
					t.Errorf("default uid = %q, want omitted", got)
				}
				if got := q.Get("gid"); got != "" {
					t.Errorf("default gid = %q, want omitted", got)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewSlicerClient(server.URL, "", "test", server.Client())
			if err := client.CpToVM(context.Background(), "vm-1", localPath, test.destination, NonRootUser, NonRootUser, "", test.mode); err != nil {
				t.Fatalf("CpToVM: %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestCpToVMRequestBodyCanBeReplayed(t *testing.T) {
	for _, mode := range []string{"binary", "tar"} {
		t.Run(mode, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if mode == "tar" {
				if err := os.Mkdir(source, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}

			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.GetBody == nil {
					t.Fatal("upload request has no GetBody function")
				}
				original, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := req.GetBody()
				if err != nil {
					t.Fatal(err)
				}
				defer replayed.Close()
				copyBody, err := io.ReadAll(replayed)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(original, copyBody) {
					t.Fatal("replayed upload body differs from the original")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			})
			client := NewSlicerClient("http://slicer.invalid", "", "test", &http.Client{Transport: transport})
			if err := client.CpToVM(t.Context(), "vm-1", source, "/tmp/destination", NonRootUser, NonRootUser, "", mode); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCpToVMFallsBackToLegacyMode(t *testing.T) {
	for _, mode := range []string{"binary", "tar"} {
		t.Run(mode, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if mode == "tar" {
				if err := os.Mkdir(source, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}

			v1Mode := cpWireModeBinaryV1
			if mode == "tar" {
				v1Mode = cpWireModeTarV1
			}
			requests := 0
			var firstBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				body, _ := io.ReadAll(r.Body)
				if requests == 1 {
					firstBody = body
					if got := r.URL.Query().Get("mode"); got != v1Mode {
						t.Errorf("first mode = %q, want %q", got, v1Mode)
					}
					if got := r.URL.Query().Get("copy_semantics"); got != cpCopySemanticsV1 {
						t.Errorf("first copy_semantics = %q", got)
					}
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":"cp produced an unexpected error: 400: invalid mode: `+v1Mode+`"}`)
					return
				}
				if got := r.URL.Query().Get("mode"); got != mode {
					t.Errorf("legacy mode = %q, want %q", got, mode)
				}
				for _, key := range []string{"copy_semantics", "source_name", "source_type", "copy_contents"} {
					if got := r.URL.Query().Get(key); got != "" {
						t.Errorf("legacy %s = %q, want omitted", key, got)
					}
				}
				if !bytes.Equal(body, firstBody) {
					t.Error("legacy upload body differs from v1 body")
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewSlicerClient(server.URL, "", "test", server.Client())
			if err := client.CpToVM(t.Context(), "vm-1", source, "/tmp/destination", NonRootUser, NonRootUser, "", mode); err != nil {
				t.Fatalf("CpToVM: %v", err)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}

func TestLegacyCopyRetryRequiresExactOldModeError(t *testing.T) {
	if !shouldRetryLegacyCopy(http.StatusBadRequest, []byte("invalid mode: cp-v1-binary"), cpWireModeBinaryV1) {
		t.Fatal("exact old-agent mode error did not trigger retry")
	}
	if shouldRetryLegacyCopy(http.StatusBadRequest, []byte("invalid mode"), cpWireModeBinaryV1) {
		t.Fatal("generic bad request triggered retry")
	}
	if shouldRetryLegacyCopy(http.StatusInternalServerError, []byte("invalid mode: cp-v1-binary"), cpWireModeBinaryV1) {
		t.Fatal("non-400 response triggered retry")
	}
}

func TestCpToVMZeroOwnershipMeansRoot(t *testing.T) {
	for _, mode := range []string{"binary", "tar"} {
		t.Run(mode, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if mode == "tar" {
				if err := os.Mkdir(source, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if got := r.URL.Query().Get("uid"); got != "0" {
					t.Errorf("uid = %q, want 0", got)
				}
				if got := r.URL.Query().Get("gid"); got != "0" {
					t.Errorf("gid = %q, want 0", got)
				}
			}))
			defer server.Close()

			client := NewSlicerClient(server.URL, "", "test", server.Client())
			if err := client.CpToVM(context.Background(), "vm-1", source, "/tmp/destination", 0, 0, "", mode); err != nil {
				t.Fatalf("CpToVM: %v", err)
			}
		})
	}
}

func TestLocalCopySourceMetadataTreatsRootAsContents(t *testing.T) {
	metadata, err := localCopySourceMetadata(string(filepath.Separator), string(filepath.Separator), "tar")
	if err != nil {
		t.Fatalf("localCopySourceMetadata: %v", err)
	}
	if !metadata.copyContents || metadata.name != "" || metadata.typeName != copySourceTypeDir {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestLocalCopySourceMetadataFollowsBinaryFileSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	metadata, err := localCopySourceMetadata(link, link, "binary")
	if err != nil {
		t.Fatalf("localCopySourceMetadata: %v", err)
	}
	if metadata.name != "link" || metadata.typeName != copySourceTypeFile {
		t.Fatalf("metadata = %+v", metadata)
	}
	if _, err := localCopySourceMetadata(link, link, "tar"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("tar error = %v", err)
	}
}

func TestWindowsStyleCopySuffixes(t *testing.T) {
	if !hasCopyContentsSuffix(`C:\work\.`) {
		t.Fatal(`C:\work\. was not recognised as a contents-only source`)
	}
	if !hasPathSeparatorSuffix(`C:\work\`) {
		t.Fatal(`C:\work\ was not recognised as directory syntax`)
	}
}

func TestCpFromVMBinaryShapesExistingDirectory(t *testing.T) {
	destination := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("copy_semantics"); got != cpCopySemanticsV1 {
			t.Errorf("copy_semantics = %q", got)
		}
		w.Header().Set(copySourceTypeHeader, copySourceTypeFile)
		w.Header().Set(copySourceNameHeader, encodeCopySourceName("tool"))
		_, _ = io.WriteString(w, "contents")
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	if err := client.CpFromVM(context.Background(), "vm-1", "~/bin/tool", destination, "", "binary"); err != nil {
		t.Fatalf("CpFromVM: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "contents" {
		t.Fatalf("contents = %q", data)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestCpFromVMBinaryFallsBackAndShapesExistingDirectory(t *testing.T) {
	destination := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if got := r.URL.Query().Get("mode"); got != cpWireModeBinaryV1 {
				t.Errorf("first mode = %q", got)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"cp produced an unexpected error: 400: invalid mode: cp-v1-binary"}`)
			return
		}
		if got := r.URL.Query().Get("mode"); got != "binary" {
			t.Errorf("legacy mode = %q", got)
		}
		if got := r.URL.Query().Get("copy_semantics"); got != "" {
			t.Errorf("legacy copy_semantics = %q, want omitted", got)
		}
		_, _ = io.WriteString(w, "guide")
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	if err := client.CpFromVM(t.Context(), "vm-1", "~/papermaking-guide.html", destination, "", "binary"); err != nil {
		t.Fatalf("CpFromVM: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "papermaking-guide.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "guide" {
		t.Fatalf("contents = %q", data)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestCpFromVMTarFallsBackToLegacyMode(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		name := "tar"
		v1Mode := cpWireModeTarV1
		wantRequests := 2
		if recursive {
			name = "recursive directory"
			v1Mode = cpWireModeRecursiveV1
			wantRequests = 3
		}
		t.Run(name, func(t *testing.T) {
			destination := t.TempDir()
			archive := makeCopyTestArchive(t, "file.txt", "contents")
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch requests {
				case 1:
					if got := r.URL.Query().Get("mode"); got != v1Mode {
						t.Errorf("first mode = %q, want %q", got, v1Mode)
					}
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":"cp produced an unexpected error: 400: invalid mode: `+v1Mode+`"}`)
				case 2:
					if recursive {
						if got := r.URL.Query().Get("mode"); got != "binary" {
							t.Errorf("legacy probe mode = %q", got)
						}
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":"cp produced an unexpected error: 400: must specify a file not a directory"}`)
						return
					}
					fallthrough
				default:
					if got := r.URL.Query().Get("mode"); got != "tar" {
						t.Errorf("legacy mode = %q", got)
					}
					if got := r.URL.Query().Get("copy_semantics"); got != "" {
						t.Errorf("legacy copy_semantics = %q, want omitted", got)
					}
					_, _ = w.Write(archive)
				}
			}))
			defer server.Close()

			client := NewSlicerClient(server.URL, "", "test", server.Client())
			mode := "tar"
			if recursive {
				mode = "recursive"
			}
			if err := client.CpFromVM(t.Context(), "vm-1", "/work/project", destination, "", mode); err != nil {
				t.Fatalf("CpFromVM: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "project", "file.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "contents" {
				t.Fatalf("contents = %q", data)
			}
			if requests != wantRequests {
				t.Fatalf("requests = %d, want %d", requests, wantRequests)
			}
		})
	}
}

func TestCpFromVMRecursiveFallsBackToLegacyBinaryForFile(t *testing.T) {
	destination := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"cp produced an unexpected error: 400: invalid mode: cp-v1-recursive"}`)
			return
		}
		if got := r.URL.Query().Get("mode"); got != "binary" {
			t.Errorf("legacy mode = %q", got)
		}
		_, _ = io.WriteString(w, "notes")
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	if err := client.CpFromVM(t.Context(), "vm-1", "/tmp/notes.txt", destination, "", "recursive"); err != nil {
		t.Fatalf("CpFromVM: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "notes" {
		t.Fatalf("contents = %q", data)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func makeCopyTestArchive(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	data := []byte(contents)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCopyMetadataPreservesSourceNameWhitespace(t *testing.T) {
	for _, name := range []string{" report.txt", "report.txt "} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(copySourceTypeHeader, copySourceTypeFile)
				w.Header().Set(copySourceNameHeader, encodeCopySourceName(name))
			}))
			defer server.Close()

			res, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatalf("GET metadata: %v", err)
			}
			defer res.Body.Close()

			metadata, err := copyMetadataFromResponse(res, false)
			if err != nil {
				t.Fatalf("copyMetadataFromResponse: %v", err)
			}
			if metadata.name != name {
				t.Fatalf("source name = %q, want %q", metadata.name, name)
			}
		})
	}
}

func TestCopyMetadataRejectsLegacyRawSourceName(t *testing.T) {
	res := &http.Response{Header: make(http.Header)}
	res.Header.Set(copySourceTypeHeader, copySourceTypeFile)
	res.Header.Set("X-Slicer-Source-Name", "tool")

	if _, err := copyMetadataFromResponse(res, false); err == nil || !strings.Contains(err.Error(), "incomplete copy metadata") {
		t.Fatalf("copyMetadataFromResponse error = %v", err)
	}
}

func TestCpFromVMBinaryRejectsResponseWithoutMetadata(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "renamed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "legacy")
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	err := client.CpFromVM(context.Background(), "vm-1", "/tmp/source", destination, "", "binary")
	if err == nil || !strings.Contains(err.Error(), "did not include cp-v1 metadata") {
		t.Fatalf("error = %v", err)
	}
}

func TestCpFromVMUsesFailClosedWireMode(t *testing.T) {
	destination := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mode"); got != cpWireModeBinaryV1 {
			t.Errorf("mode = %q", got)
		}
		http.Error(w, "invalid mode", http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	err := client.CpFromVM(context.Background(), "vm-1", "~/bin/tool", destination, "", "binary")
	if err == nil || !strings.Contains(err.Error(), "400 Bad Request") {
		t.Fatalf("error = %v", err)
	}
}

func TestCpFromVMRecursiveDispatchesRegularFile(t *testing.T) {
	destination := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mode"); got != cpWireModeRecursiveV1 {
			t.Errorf("mode = %q", got)
		}
		w.Header().Set(copySourceTypeHeader, copySourceTypeFile)
		w.Header().Set(copySourceNameHeader, encodeCopySourceName("notes.txt"))
		_, _ = io.WriteString(w, "notes")
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	if err := client.CpFromVM(context.Background(), "vm-1", "/tmp/notes.txt", destination, "", "recursive"); err != nil {
		t.Fatalf("CpFromVM: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "notes" {
		t.Fatalf("contents = %q", data)
	}
}

func TestCpFromVMTarRejectsRegularFileMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(copySourceTypeHeader, copySourceTypeFile)
		w.Header().Set(copySourceNameHeader, encodeCopySourceName("notes.txt"))
		_, _ = io.WriteString(w, "notes")
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test", server.Client())
	err := client.CpFromVM(context.Background(), "vm-1", "/tmp/notes.txt", t.TempDir(), "", "tar")
	if err == nil || !strings.Contains(err.Error(), "tar copy returned source type") {
		t.Fatalf("error = %v", err)
	}
}

func TestCpFromVMTarShapesDirectoryAndContents(t *testing.T) {
	makeArchive := func(t *testing.T) []byte {
		t.Helper()
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		data := []byte("contents")
		if err := tw.WriteHeader(&tar.Header{Name: "file.txt", Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	for _, test := range []struct {
		name       string
		remotePath string
		wantRel    string
	}{
		{name: "directory", remotePath: "/work/project", wantRel: filepath.Join("project", "file.txt")},
		{name: "contents", remotePath: "/work/project/.", wantRel: "file.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := t.TempDir()
			archive := makeArchive(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(copySourceTypeHeader, copySourceTypeDir)
				w.Header().Set(copySourceNameHeader, encodeCopySourceName("project"))
				_, _ = w.Write(archive)
			}))
			defer server.Close()

			client := NewSlicerClient(server.URL, "", "test", server.Client())
			if err := client.CpFromVM(context.Background(), "vm-1", test.remotePath, destination, "", "tar"); err != nil {
				t.Fatalf("CpFromVM: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(destination, test.wantRel))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(data)) != "contents" {
				t.Fatalf("contents = %q", data)
			}
		})
	}
}

func TestPrepareLocalTarDestination(t *testing.T) {
	t.Run("creates missing directory", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "missing")

		got, err := prepareLocalTarDestination(dest)
		if err != nil {
			t.Fatalf("prepareLocalTarDestination() error = %v", err)
		}

		if got != dest {
			t.Fatalf("prepareLocalTarDestination() = %q, want %q", got, dest)
		}

		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("expected destination to exist: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("expected destination to be a directory")
		}
	})

	t.Run("accepts existing directory", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "existing")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}

		got, err := prepareLocalTarDestination(dest)
		if err != nil {
			t.Fatalf("prepareLocalTarDestination() error = %v", err)
		}
		if got != dest {
			t.Fatalf("prepareLocalTarDestination() = %q, want %q", got, dest)
		}
	})

	t.Run("rejects existing file", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(dest, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		if _, err := prepareLocalTarDestination(dest); err == nil {
			t.Fatal("expected error for file destination")
		}
	})
}

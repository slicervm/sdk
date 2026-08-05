package slicer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrChunkedCopyUnsupported = errors.New("chunked copy is not supported by the guest agent")

const slicerAgentPath = "/usr/local/bin/slicer-agent"

type copyChunkUpload struct {
	index  int
	offset int64
}

type preparedCopySource struct {
	file         *os.File
	size         int64
	unpackedSize int64
	cleanup      func() error
}

type byteCounter struct {
	n int64
}

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// SupportsChunkedCopy reports whether the guest agent provides the upload
// finaliser required by CpToVMChunked.
func (c *SlicerClient) SupportsChunkedCopy(ctx context.Context, vmName string) (bool, error) {
	_, supported, err := c.chunkedCopyManifestVersion(ctx, vmName)
	return supported, err
}

func (c *SlicerClient) chunkedCopyManifestVersion(ctx context.Context, vmName string) (int, bool, error) {
	result, err := c.ExecBuffered(ctx, vmName, SlicerExecRequest{
		Command: slicerAgentPath,
		Args:    []string{"upload", "check"},
		UID:     0,
		GID:     0,
		Stdout:  true,
		Stderr:  true,
	})
	if err != nil {
		return 0, false, err
	}
	if result.ExitCode != 0 {
		return 0, false, nil
	}
	for _, field := range strings.Fields(result.Stdout) {
		if field == "chunked-copy-v2" {
			return ChunkedCopyManifestV2, true, nil
		}
	}
	return ChunkedCopyManifestVersion, true, nil
}

// CpToVMChunked copies a file or staged tar stream in bounded, checksummed
// requests, then asks slicer-agent to validate and assemble
// the manifest inside the guest.
func (c *SlicerClient) CpToVMChunked(ctx context.Context, vmName, localPath, vmPath string, opts ChunkedCopyOptions) error {
	if opts.ChunkSize == 0 {
		opts.ChunkSize = DefaultCopyChunkSize
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = DefaultCopyConcurrency
	}
	if opts.ChunkSize < 1 || opts.ChunkSize > 64<<20 {
		return fmt.Errorf("chunk size must be between 1 byte and 64 MiB")
	}
	if opts.Concurrency < 1 || opts.Concurrency > 16 {
		return fmt.Errorf("copy concurrency must be between 1 and 16")
	}
	if opts.Mode != "binary" && opts.Mode != "tar" {
		return fmt.Errorf("invalid mode: %s", opts.Mode)
	}

	manifestVersion, supported, err := c.chunkedCopyManifestVersion(ctx, vmName)
	if err != nil {
		return fmt.Errorf("check guest chunked-copy support: %w", err)
	}
	if !supported {
		return ErrChunkedCopyUnsupported
	}

	absSrc, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("get absolute source path: %w", err)
	}
	metadata, err := localCopySourceMetadata(localPath, absSrc, opts.Mode)
	if err != nil {
		return err
	}
	sessionID, err := newCopySessionID()
	if err != nil {
		return err
	}
	sessionPath, err := copySessionPath(vmPath, sessionID)
	if err != nil {
		return err
	}
	manifestPath := pathpkg.Join(sessionPath, "manifest.json")

	source, err := prepareChunkedCopySource(ctx, absSrc, opts)
	if err != nil {
		return err
	}
	defer source.cleanup()

	complete := false
	defer func() {
		if complete {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.abortChunkedCopy(cleanupCtx, vmName, manifestPath, opts.UID, opts.GID)
	}()

	manifest := CopyManifest{
		Version:      manifestVersion,
		Mode:         opts.Mode,
		Destination:  vmPath,
		UID:          opts.UID,
		GID:          opts.GID,
		Permissions:  opts.Permissions,
		UnpackedSize: source.unpackedSize,
		Chunks:       []CopyChunk{},
	}
	if manifestVersion >= ChunkedCopyManifestV2 {
		manifest.CopySemantics = cpCopySemanticsV1
		manifest.SourceName = metadata.name
		manifest.SourceType = metadata.typeName
		manifest.CopyContents = metadata.copyContents
	}
	for offset := int64(0); offset < source.size; offset += int64(opts.ChunkSize) {
		size := int64(opts.ChunkSize)
		if remaining := source.size - offset; remaining < size {
			size = remaining
		}
		manifest.Chunks = append(manifest.Chunks, CopyChunk{
			Index: len(manifest.Chunks),
			Size:  size,
		})
	}
	manifest.Size = source.size
	if err := c.uploadCopyChunks(ctx, vmName, sessionPath, opts, source.file, &manifest); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate generated copy manifest: %w", err)
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode copy manifest: %w", err)
	}
	if err := c.uploadCopyBytes(ctx, vmName, manifestPath, opts.UID, opts.GID, manifestData); err != nil {
		return fmt.Errorf("upload copy manifest: %w", err)
	}

	result, err := c.ExecBuffered(ctx, vmName, SlicerExecRequest{
		Command: slicerAgentPath,
		Args: []string{
			"upload", "finalise",
			"--manifest", manifestPath,
			"--uid", strconv.FormatUint(uint64(opts.UID), 10),
			"--gid", strconv.FormatUint(uint64(opts.GID), 10),
		},
		UID:    0,
		GID:    0,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("finalise chunked copy: %w", err)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = strings.TrimSpace(result.Error)
		}
		return fmt.Errorf("finalise chunked copy: exit code %d: %s", result.ExitCode, message)
	}

	complete = true
	return nil
}

func (c *SlicerClient) uploadCopyChunks(ctx context.Context, vmName, sessionPath string, opts ChunkedCopyOptions, source *os.File, manifest *CopyManifest) error {
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan copyChunkUpload)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	workerCount := opts.Concurrency
	if len(manifest.Chunks) < workerCount {
		workerCount = len(manifest.Chunks)
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				chunk := &manifest.Chunks[job.index]
				section := io.NewSectionReader(source, job.offset, chunk.Size)
				hasher := sha256.New()
				counter := &byteCounter{}
				reader := io.TeeReader(section, io.MultiWriter(hasher, counter))
				chunkPath := pathpkg.Join(sessionPath, "chunks", CopyChunkFileName(*chunk))
				err := c.uploadCopyReader(uploadCtx, vmName, chunkPath, opts.UID, opts.GID, reader, chunk.Size)
				if err == nil && counter.n != chunk.Size {
					err = fmt.Errorf("streamed %d bytes, expected %d", counter.n, chunk.Size)
				}
				if err != nil {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("upload chunk %d: %w", chunk.Index, err)
						cancel()
					})
					continue
				}
				chunk.SHA256 = hex.EncodeToString(hasher.Sum(nil))
			}
		}()
	}

	offset := int64(0)
	for i := range manifest.Chunks {
		select {
		case jobs <- copyChunkUpload{index: i, offset: offset}:
			offset += manifest.Chunks[i].Size
		case <-uploadCtx.Done():
			close(jobs)
			wg.Wait()
			if firstErr != nil {
				return firstErr
			}
			return uploadCtx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return firstErr
}

func (c *SlicerClient) uploadCopyBytes(ctx context.Context, vmName, vmPath string, uid, gid uint32, data []byte) error {
	return c.uploadCopyReader(ctx, vmName, vmPath, uid, gid, bytes.NewReader(data), int64(len(data)))
}

func (c *SlicerClient) uploadCopyReader(ctx context.Context, vmName, vmPath string, uid, gid uint32, reader io.Reader, size int64) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("parse Slicer URL: %w", err)
	}
	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)
	q.Set("mode", "binary")
	q.Set("permissions", "0600")
	if uid != NonRootUser {
		q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	}
	if gid != NonRootUser {
		q.Set("gid", strconv.FormatUint(uint64(gid), 10))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), reader)
	if err != nil {
		return fmt.Errorf("create chunk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform chunk request: %w", err)
	}
	if res.Body != nil {
		defer func() {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}()
	}
	if res.StatusCode != http.StatusOK {
		var body []byte
		if res.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(res.Body, 64<<10))
		}
		return fmt.Errorf("copy chunk to VM: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *SlicerClient) abortChunkedCopy(ctx context.Context, vmName, manifestPath string, uid, gid uint32) error {
	result, err := c.ExecBuffered(ctx, vmName, SlicerExecRequest{
		Command: slicerAgentPath,
		Args: []string{
			"upload", "abort",
			"--manifest", manifestPath,
			"--uid", strconv.FormatUint(uint64(uid), 10),
			"--gid", strconv.FormatUint(uint64(gid), 10),
		},
		UID:    0,
		GID:    0,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("abort chunked copy: exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func prepareChunkedCopySource(ctx context.Context, absSrc string, opts ChunkedCopyOptions) (preparedCopySource, error) {
	if opts.Mode == "binary" {
		f, err := os.Open(absSrc)
		if err != nil {
			return preparedCopySource{}, fmt.Errorf("open binary source: %w", err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return preparedCopySource{}, fmt.Errorf("stat binary source: %w", err)
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			return preparedCopySource{}, fmt.Errorf("binary source must be a regular file: %s", absSrc)
		}
		return preparedCopySource{
			file: f,
			size: info.Size(),
			cleanup: func() error {
				return f.Close()
			},
		}, nil
	}

	parentDir := filepath.Dir(absSrc)
	baseName := filepath.Base(absSrc)
	unpackedSize, err := EstimateTarUnpackedSize(ctx, parentDir, baseName, opts.ExcludePatterns...)
	if err != nil {
		return preparedCopySource{}, fmt.Errorf("estimate tar size: %w", err)
	}
	// Keep the staged archive outside the source tree. Staging beside the
	// source makes a root/contents copy capable of archiving its own growing
	// tar file, and unnecessarily requires the source parent to be writable.
	staged, err := os.CreateTemp("", ".slicer-upload-*.tar")
	if err != nil {
		return preparedCopySource{}, fmt.Errorf("create staged tar: %w", err)
	}
	removeStaged := func() {
		_ = staged.Close()
		_ = os.Remove(staged.Name())
	}
	if err := StreamTarArchive(ctx, staged, parentDir, baseName, opts.ExcludePatterns...); err != nil {
		removeStaged()
		return preparedCopySource{}, fmt.Errorf("create staged tar: %w", err)
	}
	info, err := staged.Stat()
	if err != nil {
		removeStaged()
		return preparedCopySource{}, fmt.Errorf("stat staged tar: %w", err)
	}
	return preparedCopySource{
		file:         staged,
		size:         info.Size(),
		unpackedSize: unpackedSize,
		cleanup: func() error {
			closeErr := staged.Close()
			removeErr := os.Remove(staged.Name())
			if closeErr != nil {
				return closeErr
			}
			return removeErr
		},
	}, nil
}

func newCopySessionID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate copy session ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func copySessionPath(vmPath, sessionID string) (string, error) {
	clean := pathpkg.Clean(strings.TrimSpace(vmPath))
	if clean == "." || clean == "/" || clean == "" {
		return "", fmt.Errorf("chunked copy requires a named destination path")
	}
	parent := pathpkg.Dir(clean)
	if clean == "~" {
		parent = "~"
	}
	if parent == "." && !strings.HasPrefix(clean, "/") && !strings.HasPrefix(clean, "~") {
		return "", fmt.Errorf("chunked copy destination must be absolute or home-relative: %s", vmPath)
	}
	return pathpkg.Join(parent, ".slicer-upload-"+sessionID), nil
}

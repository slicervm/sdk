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
	descriptor CopyChunk
	data       []byte
}

// SupportsChunkedCopy reports whether the guest agent provides the upload
// finaliser required by CpToVMChunked.
func (c *SlicerClient) SupportsChunkedCopy(ctx context.Context, vmName string) (bool, error) {
	result, err := c.ExecBuffered(ctx, vmName, SlicerExecRequest{
		Command: slicerAgentPath,
		Args:    []string{"upload", "check"},
		UID:     0,
		GID:     0,
		Stdout:  true,
		Stderr:  true,
	})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// CpToVMChunked copies a file or generated tar stream in bounded,
// content-addressed requests, then asks slicer-agent to validate and assemble
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

	supported, err := c.SupportsChunkedCopy(ctx, vmName)
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
	info, err := os.Stat(absSrc)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if opts.Mode == "binary" && !info.Mode().IsRegular() {
		return fmt.Errorf("binary source must be a regular file: %s", localPath)
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

	source, unpackedSize, waitSource, err := openChunkedCopySource(ctx, absSrc, opts)
	if err != nil {
		return err
	}
	defer source.Close()

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
		Version:      ChunkedCopyManifestVersion,
		Mode:         opts.Mode,
		Destination:  vmPath,
		UID:          opts.UID,
		GID:          opts.GID,
		Permissions:  opts.Permissions,
		UnpackedSize: unpackedSize,
		Chunks:       []CopyChunk{},
	}
	wholeHash := sha256.New()
	readComplete := false

	for !readComplete {
		batch := make([]copyChunkUpload, 0, opts.Concurrency)
		for len(batch) < opts.Concurrency && !readComplete {
			buf := make([]byte, opts.ChunkSize)
			n, readErr := io.ReadFull(source, buf)
			switch readErr {
			case nil:
			case io.EOF, io.ErrUnexpectedEOF:
				readComplete = true
			default:
				return fmt.Errorf("read copy stream: %w", readErr)
			}
			if n == 0 {
				continue
			}
			buf = buf[:n]
			_, _ = wholeHash.Write(buf)
			sum := sha256.Sum256(buf)
			chunk := CopyChunk{
				Index:  len(manifest.Chunks),
				Size:   int64(n),
				SHA256: hex.EncodeToString(sum[:]),
			}
			manifest.Chunks = append(manifest.Chunks, chunk)
			manifest.Size += int64(n)
			batch = append(batch, copyChunkUpload{descriptor: chunk, data: buf})
		}

		if err := c.uploadCopyChunkBatch(ctx, vmName, sessionPath, opts, batch); err != nil {
			return err
		}
	}

	if err := waitSource(); err != nil {
		return fmt.Errorf("produce copy stream: %w", err)
	}
	manifest.SHA256 = hex.EncodeToString(wholeHash.Sum(nil))
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

func (c *SlicerClient) uploadCopyChunkBatch(ctx context.Context, vmName, sessionPath string, opts ChunkedCopyOptions, batch []copyChunkUpload) error {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for _, item := range batch {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunkPath := pathpkg.Join(sessionPath, "chunks", CopyChunkFileName(item.descriptor))
			if err := c.uploadCopyBytes(ctx, vmName, chunkPath, opts.UID, opts.GID, item.data); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("upload chunk %d: %w", item.descriptor.Index, err)
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func (c *SlicerClient) uploadCopyBytes(ctx context.Context, vmName, vmPath string, uid, gid uint32, data []byte) error {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create chunk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
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
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
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

func openChunkedCopySource(ctx context.Context, absSrc string, opts ChunkedCopyOptions) (io.ReadCloser, int64, func() error, error) {
	if opts.Mode == "binary" {
		f, err := os.Open(absSrc)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("open binary source: %w", err)
		}
		return f, 0, func() error { return nil }, nil
	}

	parentDir := filepath.Dir(absSrc)
	baseName := filepath.Base(absSrc)
	unpackedSize, err := EstimateTarUnpackedSize(ctx, parentDir, baseName, opts.ExcludePatterns...)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("estimate tar size: %w", err)
	}
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := StreamTarArchive(ctx, pw, parentDir, baseName, opts.ExcludePatterns...)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		errCh <- err
	}()
	return pr, unpackedSize, func() error { return <-errCh }, nil
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

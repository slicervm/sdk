package slicer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
)

const fileModeHeader = "X-Slicer-File-Mode"
const unpackedSizeHeader = "X-Slicer-Unpacked-Size"

const (
	cpCopySemanticsV1     = "cp-v1"
	cpWireModeBinaryV1    = "cp-v1-binary"
	cpWireModeTarV1       = "cp-v1-tar"
	cpWireModeRecursiveV1 = "cp-v1-recursive"
	copySourceTypeFile    = "file"
	copySourceTypeDir     = "directory"
	copySourceTypeHeader  = "X-Slicer-Source-Type"
	copySourceNameHeader  = "X-Slicer-Source-Name"
)

type copySourceMetadata struct {
	name         string
	typeName     string
	copyContents bool
}

func setCopySemanticsQuery(q url.Values, metadata copySourceMetadata) {
	q.Set("copy_semantics", cpCopySemanticsV1)
	q.Set("source_name", metadata.name)
	q.Set("source_type", metadata.typeName)
	if metadata.copyContents {
		q.Set("copy_contents", "true")
	}
}

func requestCopySemantics(q url.Values) {
	q.Set("copy_semantics", cpCopySemanticsV1)
}

func localCopySourceMetadata(localPath, absSrc, mode string) (copySourceMetadata, error) {
	info, err := os.Lstat(absSrc)
	if err != nil {
		return copySourceMetadata{}, fmt.Errorf("source does not exist: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copySourceMetadata{}, fmt.Errorf("source must not be a symbolic link: %s", localPath)
	}
	metadata := copySourceMetadata{
		name:         filepath.Base(absSrc),
		copyContents: hasCopyContentsSuffix(localPath) || filepath.Dir(absSrc) == absSrc,
	}
	if metadata.copyContents && !validCopySourceName(metadata.name) {
		metadata.name = ""
	}
	if !metadata.copyContents && !validCopySourceName(metadata.name) {
		return copySourceMetadata{}, fmt.Errorf("source must have a named path: %s", localPath)
	}
	switch mode {
	case "binary":
		if !info.Mode().IsRegular() {
			return copySourceMetadata{}, fmt.Errorf("binary source must be a regular file: %s", localPath)
		}
		metadata.typeName = copySourceTypeFile
		metadata.copyContents = false
	case "tar":
		if !info.IsDir() {
			return copySourceMetadata{}, fmt.Errorf("tar source must be a directory: %s", localPath)
		}
		metadata.typeName = copySourceTypeDir
	default:
		return copySourceMetadata{}, fmt.Errorf("invalid mode: %s", mode)
	}
	return metadata, nil
}

// getCurrentUIDGID returns the current user's UID and GID.
// On Windows, returns 0,0 (chown operations will be skipped).
func getCurrentUIDGID() (uid, gid uint32) {
	if currentUser, err := user.Current(); err == nil {
		if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
			uid = uint32(parsedUID)
		}
		if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
			gid = uint32(parsedGID)
		}
	}
	return uid, gid
}

// setAuthHeaders sets User-Agent and Authorization headers on the request.
func (c *SlicerClient) setAuthHeaders(req *http.Request) {
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func copyToVMBinary(ctx context.Context, c *SlicerClient, absSrc, vmName, vmPath string, metadata copySourceMetadata, uid, gid uint32, permissions string) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)
	q.Set("mode", cpWireModeBinaryV1)
	setCopySemanticsQuery(q, metadata)

	if uid != NonRootUser {
		q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	}
	if gid != NonRootUser {
		q.Set("gid", strconv.FormatUint(uint64(gid), 10))
	}

	if len(permissions) > 0 {
		q.Set("permissions", permissions)
	}

	u.RawQuery = q.Encode()

	f, err := os.Open(absSrc)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), f)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/octet-stream")
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform POST request: %w", err)
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
			body, _ = io.ReadAll(res.Body)
		}
		return fmt.Errorf("failed to copy to VM: %s: %s", res.Status, string(body))
	}

	return nil
}

func copyToVMTar(ctx context.Context, c *SlicerClient, absSrc, vmName, vmPath string, metadata copySourceMetadata, uid, gid uint32, permissions string, excludePatterns ...string) error {
	parentDir := filepath.Dir(absSrc)
	baseName := filepath.Base(absSrc)

	unpackedSize, err := EstimateTarUnpackedSize(ctx, parentDir, baseName, excludePatterns...)
	if err != nil {
		return fmt.Errorf("failed to estimate tar unpacked size: %w", err)
	}

	pr, pw := io.Pipe()
	defer pr.Close()

	go func() {
		defer pw.Close()
		if err := StreamTarArchive(ctx, pw, parentDir, baseName, excludePatterns...); err != nil {
			pw.CloseWithError(fmt.Errorf("failed to stream tar: %w", err))
		}
	}()

	q := url.Values{}
	q.Set("path", vmPath)
	q.Set("mode", cpWireModeTarV1)
	setCopySemanticsQuery(q, metadata)
	if uid != NonRootUser {
		q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	}
	if gid != NonRootUser {
		q.Set("gid", strconv.FormatUint(uint64(gid), 10))
	}
	if len(permissions) > 0 {
		q.Set("permissions", permissions)
	}
	for _, pattern := range excludePatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		q.Add("exclude", pattern)
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), pr)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-tar")
	req.Header.Set(unpackedSizeHeader, strconv.FormatInt(unpackedSize, 10))
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform POST request: %w", err)
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
			body, _ = io.ReadAll(res.Body)
		}
		return fmt.Errorf("failed to copy to VM: %s: %s", res.Status, string(body))
	}

	return nil
}

func copyFromVMTar(ctx context.Context, c *SlicerClient, vmName, vmPath, localPath, permissions string, recursive bool, excludePatterns ...string) error {
	q := url.Values{}
	q.Set("path", vmPath)
	if recursive {
		q.Set("mode", cpWireModeRecursiveV1)
	} else {
		q.Set("mode", cpWireModeTarV1)
	}
	requestCopySemantics(q)
	for _, pattern := range excludePatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		q.Add("exclude", pattern)
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}
	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if recursive {
		req.Header.Set("Accept", "application/octet-stream, application/x-tar")
	} else {
		req.Header.Set("Accept", "application/x-tar")
	}
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform GET request: %w", err)
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
			body, _ = io.ReadAll(res.Body)
		}
		return fmt.Errorf("failed to copy from VM: %s: %s", res.Status, string(body))
	}

	copyContents := hasCopyContentsSuffix(vmPath) || pathpkg.Dir(pathpkg.Clean(vmPath)) == pathpkg.Clean(vmPath)
	metadata, err := copyMetadataFromResponse(res, copyContents)
	if err != nil {
		return err
	}
	if metadata == nil {
		return fmt.Errorf("copy response did not include cp-v1 metadata")
	}
	if metadata != nil && metadata.typeName == copySourceTypeFile {
		if !recursive {
			return fmt.Errorf("tar copy returned source type %q", metadata.typeName)
		}
		return writeBinaryCopyResponse(res, localPath, permissions, metadata)
	}

	destDir := ""
	if metadata == nil {
		destDir, err = prepareLocalTarDestination(localPath)
	} else {
		if metadata.typeName != copySourceTypeDir {
			return fmt.Errorf("tar copy returned source type %q", metadata.typeName)
		}
		metadata.copyContents = copyContents
		destDir, err = prepareLocalDirectoryDestination(localPath, *metadata)
	}
	if err != nil {
		return err
	}

	uid, gid := getCurrentUIDGID()

	return ExtractTarToPath(ctx, res.Body, destDir, uid, gid, excludePatterns...)
}

func prepareLocalTarDestination(localPath string) (string, error) {
	info, err := os.Stat(localPath)
	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("destination must be a directory in tar mode: %s", localPath)
		}
		return localPath, nil
	}

	if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to stat tar destination: %w", err)
	}

	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return "", fmt.Errorf("failed to create tar destination directory: %w", err)
	}

	return localPath, nil
}

func copyFromVMBinary(ctx context.Context, c *SlicerClient, vmName, vmPath, localPath string, permissions string) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)
	q.Set("mode", cpWireModeBinaryV1)
	requestCopySemantics(q)

	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/octet-stream")
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}

	if res.Body != nil {
		defer func() {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}()
	}

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("failed to copy from VM: %s: %s", res.Status, string(body))
	}

	metadata, err := copyMetadataFromResponse(res, false)
	if err != nil {
		return err
	}
	if metadata == nil {
		return fmt.Errorf("copy response did not include cp-v1 metadata")
	}
	return writeBinaryCopyResponse(res, localPath, permissions, metadata)
}

func writeBinaryCopyResponse(res *http.Response, localPath, permissions string, metadata *copySourceMetadata) error {
	var err error
	if metadata != nil {
		if metadata.typeName != copySourceTypeFile {
			return fmt.Errorf("binary copy returned source type %q", metadata.typeName)
		}
		localPath, err = resolveLocalFileDestination(localPath, metadata.name)
		if err != nil {
			return err
		}
	}

	fileMode := os.FileMode(0600)
	if len(permissions) > 0 {
		fileMode, err = parseFileMode(permissions)
		if err != nil {
			return fmt.Errorf("invalid permissions format: %w", err)
		}
	} else if mode := strings.TrimSpace(res.Header.Get(fileModeHeader)); mode != "" {
		fileMode, err = parseFileMode(mode)
		if err != nil {
			return fmt.Errorf("invalid mode returned by server: %w", err)
		}
	}

	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer f.Close()

	if res.Body == nil {
		return fmt.Errorf("no body received from VM")
	}

	if _, err = io.Copy(f, res.Body); err != nil {
		return fmt.Errorf("failed to write to local file: %w", err)
	}

	return nil
}

func copyMetadataFromResponse(res *http.Response, copyContents bool) (*copySourceMetadata, error) {
	typeName := strings.TrimSpace(res.Header.Get(copySourceTypeHeader))
	name := strings.TrimSpace(res.Header.Get(copySourceNameHeader))
	if typeName == "" && name == "" {
		return nil, nil
	}
	if typeName == "" || (name == "" && !copyContents) {
		return nil, fmt.Errorf("incomplete copy metadata returned by server")
	}
	if typeName != copySourceTypeFile && typeName != copySourceTypeDir {
		return nil, fmt.Errorf("invalid source type returned by server: %q", typeName)
	}
	if !copyContents && !validCopySourceName(name) {
		return nil, fmt.Errorf("invalid source name returned by server: %q", name)
	}
	return &copySourceMetadata{name: name, typeName: typeName, copyContents: copyContents}, nil
}

func validCopySourceName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\\`)
}

func hasCopyContentsSuffix(source string) bool {
	normalized := strings.ReplaceAll(source, `\`, "/")
	normalized = strings.TrimRight(normalized, "/")
	return strings.HasSuffix(normalized, "/.")
}

func hasPathSeparatorSuffix(name string) bool {
	return strings.HasSuffix(name, "/") || strings.HasSuffix(name, `\`)
}

func resolveLocalFileDestination(localPath, sourceName string) (string, error) {
	info, err := os.Stat(localPath)
	switch {
	case err == nil && info.IsDir():
		return filepath.Join(localPath, sourceName), nil
	case err == nil:
		return localPath, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("failed to stat local destination: %w", err)
	case hasPathSeparatorSuffix(localPath):
		return "", fmt.Errorf("destination directory does not exist: %s", localPath)
	default:
		return localPath, nil
	}
}

func prepareLocalDirectoryDestination(localPath string, metadata copySourceMetadata) (string, error) {
	info, err := os.Stat(localPath)
	switch {
	case err == nil && !info.IsDir():
		return "", fmt.Errorf("destination exists and is not a directory: %s", localPath)
	case err == nil && metadata.copyContents:
		return localPath, nil
	case err == nil:
		localPath = filepath.Join(localPath, metadata.name)
	case !os.IsNotExist(err):
		return "", fmt.Errorf("failed to stat local destination: %w", err)
	}

	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return "", fmt.Errorf("failed to create local destination directory: %w", err)
	}
	return localPath, nil
}

func parseFileMode(permissions string) (os.FileMode, error) {
	permUint, err := strconv.ParseUint(permissions, 8, 32)
	if err != nil {
		return 0, err
	}

	return os.FileMode(permUint), nil
}

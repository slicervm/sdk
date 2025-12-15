package slicer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

func copyToVMBinary(ctx context.Context, c *SlicerClient, absSrc, vmName, vmPath string, uid, gid uint32, permissions string) error {

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)

	// Get current user's UID/GID if not specified
	// On Windows, this will be 0,0 and chown will be skipped
	if uid == 0 && gid == 0 {
		if currentUser, err := user.Current(); err == nil {
			if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
				uid = uint32(parsedUID)
			}
			if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
				gid = uint32(parsedGID)
			}
		}
	}

	q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	q.Set("gid", strconv.FormatUint(uint64(gid), 10))

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

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform POST request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		var body []byte
		if res.Body != nil {
			body, _ = io.ReadAll(res.Body)
		}
		return fmt.Errorf("failed to copy to VM: %s: %s", res.Status, string(body))
	}

	return nil

}

func copyToVMTar(ctx context.Context, c *SlicerClient, absSrc, vmName, vmPath string, uid, gid uint32, permissions string) error {

	// Use parentDir and baseName to strip leading paths (like cp)
	parentDir := filepath.Dir(absSrc)
	baseName := filepath.Base(absSrc)

	// Create a pipe to stream tar data
	pr, pw := io.Pipe()
	defer pr.Close() // Close reader when function returns

	// Stream tar in a goroutine
	go func() {
		defer pw.Close()
		if err := StreamTarArchive(ctx, pw, parentDir, baseName); err != nil {
			pw.CloseWithError(fmt.Errorf("failed to stream tar: %w", err))
		}
	}()

	// Make HTTP request
	q := url.Values{}
	q.Set("path", vmPath)
	if uid > 0 {
		q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	}
	if gid > 0 {
		q.Set("gid", strconv.FormatUint(uint64(gid), 10))
	}

	if len(permissions) > 0 {
		q.Set("permissions", permissions)
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

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform POST request: %w", err)
	}

	if res.Body != nil {
		defer res.Body.Close()
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

func copyFromVMTar(ctx context.Context, c *SlicerClient, vmName, vmPath, localPath string, uid, gid uint32) error {

	q := url.Values{}
	q.Set("path", vmPath)

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

	req.Header.Set("Accept", "application/x-tar")

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform GET request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		var body []byte
		if res.Body != nil {
			body, _ = io.ReadAll(res.Body)
		}
		return fmt.Errorf("failed to copy from VM: %s: %s", res.Status, string(body))
	}

	// Get current user's UID/GID if not specified
	// On Windows, this will be 0,0 and chown will be skipped
	if uid == 0 && gid == 0 {
		if currentUser, err := user.Current(); err == nil {
			if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
				uid = uint32(parsedUID)
			}
			if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
				gid = uint32(parsedGID)
			}
		}
	}

	// Extract tar stream to local path with renaming logic
	return ExtractTarToPath(ctx, res.Body, localPath, uid, gid)
}

func copyFromVMBinary(ctx context.Context, c *SlicerClient, vmName, vmPath, localPath string, uid, gid uint32, permissions string) error {

	fileMode := os.FileMode(0600)
	if len(permissions) > 0 {
		permUint, err := strconv.ParseUint(permissions, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid permissions format: %w", err)
		}
		fileMode = os.FileMode(permUint)
	}

	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer f.Close()

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)
	q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	q.Set("gid", strconv.FormatUint(uint64(gid), 10))

	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	req.Header.Set("Accept", "application/octet-stream")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}

	if res.Body != nil {
		defer res.Body.Close()
	}

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("failed to copy from VM: %s: %s", res.Status, string(body))
	}

	if res.Body == nil {
		return fmt.Errorf("no body received from VM")
	}

	if _, err = io.Copy(f, res.Body); err != nil {
		return fmt.Errorf("failed to write to local file: %w", err)
	}

	return nil
}

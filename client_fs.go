package slicer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ReadFile downloads a file from the VM and returns its contents and optional mode.
func (c *SlicerClient) ReadFile(ctx context.Context, vmName, vmPath string) ([]byte, string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode == http.StatusNotFound {
		return nil, "", os.ErrNotExist
	}

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return nil, "", fmt.Errorf("failed to read file from VM: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}

	mode := strings.TrimSpace(res.Header.Get(fileModeHeader))
	if mode == "" {
		mode = "0600"
	}

	return data, mode, nil
}

// WriteFile uploads a binary file to the VM.
func (c *SlicerClient) WriteFile(ctx context.Context, vmName, vmPath string, data []byte, uid, gid uint32, permissions string) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/cp", vmName)
	q := url.Values{}
	q.Set("path", vmPath)

	if uid == 0 && gid == 0 {
		uid, gid = getCurrentUIDGID()
	}

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

	reader := bytes.NewReader(data)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), reader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform POST request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("failed to write file to VM: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	return nil
}

// ReadDir lists entries in a VM path.
func (c *SlicerClient) ReadDir(ctx context.Context, vmName, path string) ([]SlicerFSInfo, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/fs/readdir", vmName)
	q := url.Values{}
	q.Set("path", path)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("failed to read directory: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var entries []SlicerFSInfo
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("failed to decode directory listing: %w", err)
	}

	return entries, nil
}

// Stat fetches metadata for a single path inside a VM.
func (c *SlicerClient) Stat(ctx context.Context, vmName, path string) (*SlicerFSInfo, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/fs/stat", vmName)
	q := url.Values{}
	q.Set("path", path)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode == http.StatusNotFound {
		return nil, os.ErrNotExist
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("failed to stat path: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var entry SlicerFSInfo
	if err := json.NewDecoder(res.Body).Decode(&entry); err != nil {
		return nil, fmt.Errorf("failed to decode stat result: %w", err)
	}

	return &entry, nil
}

// Exists checks if a path exists in a VM.
func (c *SlicerClient) Exists(ctx context.Context, vmName, path string) (bool, error) {
	_, err := c.Stat(ctx, vmName, path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Mkdir creates a directory in a VM.
func (c *SlicerClient) Mkdir(ctx context.Context, vmName string, request SlicerFSMkdirRequest) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/fs/mkdir", vmName)

	reqBody, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("failed to create directory: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	return nil
}

// Remove deletes a file or directory in a VM.
func (c *SlicerClient) Remove(ctx context.Context, vmName, path string, recursive bool) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/fs/remove", vmName)
	q := url.Values{}
	q.Set("path", path)
	q.Set("recursive", strconv.FormatBool(recursive))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setAuthHeaders(req)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("failed to remove path: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	return nil
}

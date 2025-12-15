package slicer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrSecretExists is an error returned when a secret with given name already exists.
	ErrSecretExists = errors.New("secret already exists")
)

// SlicerClient handles all HTTP communication with the Slicer API
type SlicerClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	userAgent  string
}

// NewSlicerClient creates a new Slicer API client
func NewSlicerClient(baseURL, token string, userAgent string, httpClient *http.Client) *SlicerClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &SlicerClient{
		httpClient: httpClient,
		baseURL:    baseURL,
		token:      token,
		userAgent:  userAgent,
	}
}

// makeJSONRequest creates and executes an HTTP request with proper authentication
func (c *SlicerClient) makeJSONRequest(method, endpoint string, body interface{}) (*http.Response, error) {
	ctx := context.Background()
	return c.makeJSONRequestWithContext(ctx, method, endpoint, body)
}

// makeJSONRequest creates and executes an HTTP request with proper authentication
func (c *SlicerClient) makeJSONRequestWithContext(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	u.Path = path.Join(u.Path, endpoint)

	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	return c.httpClient.Do(req)
}

// GetHostGroups fetches all host groups from the API
func (c *SlicerClient) GetHostGroups(ctx context.Context) ([]SlicerHostGroup, error) {
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodGet, "/hostgroup", nil)
	if err != nil {
		return nil, err
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	var hostGroups []SlicerHostGroup
	if err := json.Unmarshal(body, &hostGroups); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return hostGroups, nil
}

// GetHostGroupNodes fetches nodes for a specific host group
func (c *SlicerClient) GetHostGroupNodes(ctx context.Context, groupName string) ([]SlicerNode, error) {
	endpoint := fmt.Sprintf("hostgroup/%s/nodes", groupName)
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodes: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	var nodes []SlicerNode
	if err := json.Unmarshal(body, &nodes); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return nodes, nil
}

// CreateNode creates a new node in the specified host group
func (c *SlicerClient) CreateNode(ctx context.Context, groupName string, request SlicerCreateNodeRequest) (*SlicerCreateNodeResponse, error) {
	endpoint := fmt.Sprintf("hostgroup/%s/nodes", groupName)
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodPost, endpoint, request)
	if err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	var result SlicerCreateNodeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// DeleteNode deletes a node from the specified host group
func (c *SlicerClient) DeleteNode(groupName, nodeName string) error {
	endpoint := fmt.Sprintf("hostgroup/%s/nodes/%s", groupName, nodeName)
	res, err := c.makeJSONRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to delete node: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	return nil
}

// ListSecrets retrieves all secrets.
// Note: The actual secret data is not returned for security reasons.
func (c *SlicerClient) ListSecrets(ctx context.Context) ([]Secret, error) {
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodGet, "/secrets", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	var secrets []Secret
	if err := json.Unmarshal(body, &secrets); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return secrets, nil
}

// CreateSecret creates a new secret.
// Returns ErrSecretExists if a secret with the same name already exists.
// An error is returned if creation fails.
func (c *SlicerClient) CreateSecret(ctx context.Context, request CreateSecretRequest) error {
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodPost, "/secrets", request)
	if err != nil {
		return fmt.Errorf("failed to create secret: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode == http.StatusConflict {
		return ErrSecretExists
	}

	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	return nil
}

// PatchSecret updates an existing secret with new data and/or metadata.
// Only the fields provided in the UpdateSecretRequest will be modified.
// Returns an error if the secret doesn't exist or if the update fails.
func (c *SlicerClient) PatchSecret(ctx context.Context, secretName string, request UpdateSecretRequest) error {
	endpoint := path.Join("/secrets", secretName)
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodPatch, endpoint, request)
	if err != nil {
		return fmt.Errorf("failed to patch secret: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	return nil
}

// DeleteSecret removes a secret.
// Returns an error if the secret doesn't exist or if the deletion fails.
func (c *SlicerClient) DeleteSecret(ctx context.Context, secretName string) error {
	endpoint := path.Join("secrets", secretName)
	res, err := c.makeJSONRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to delete secret: %w", err)
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("API request failed: %s - %s", res.Status, string(body))
	}

	return nil
}

// Exec executes a command on the specified node and streams the output.
// The channel is unbuffered so the caller should read from it promptly to avoid blocking.
func (c *SlicerClient) Exec(ctx context.Context, nodeName string, execReq SlicerExecRequest) (chan SlicerExecWriteResult, error) {

	resChan := make(chan SlicerExecWriteResult)

	command := execReq.Command
	args := execReq.Args
	uid := execReq.UID
	gid := execReq.GID
	shell := execReq.Shell
	stdin := execReq.Stdin

	cwd := execReq.Cwd

	q := url.Values{}
	q.Set("cmd", command)

	for _, arg := range args {
		q.Add("args", arg)
	}

	q.Set("uid", strconv.FormatUint(uint64(uid), 10))
	q.Set("gid", strconv.FormatUint(uint64(gid), 10))

	if len(cwd) > 0 {
		q.Set("cwd", cwd)
	}

	if len(execReq.Permissions) > 0 {
		q.Set("permissions", execReq.Permissions)
	}

	var bodyReader io.Reader

	if stdin {
		q.Set("stdin", "true")
		bodyReader = os.Stdin
	}
	if len(shell) > 0 {
		q.Set("shell", shell)
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return resChan, fmt.Errorf("failed to parse API URL: %w", err)
	}
	u.Path = fmt.Sprintf("/vm/%s/exec", nodeName)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bodyReader)
	if err != nil {
		return resChan, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)

	req.URL.RawQuery = q.Encode()

	res, err := c.httpClient.Do(req)
	if err != nil {
		return resChan, fmt.Errorf("failed to execute request: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		var body []byte
		if res.Body != nil {
			defer res.Body.Close()
			body, _ = io.ReadAll(res.Body)
		}
		return resChan, fmt.Errorf("failed to execute command: %s %s", res.Status, string(body))
	}

	r := bufio.NewReader(res.Body)
	go func() {

		if res.Body != nil {
			defer res.Body.Close()
		}
		defer close(resChan)

		for {

			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := r.ReadBytes('\n')
			if err == io.EOF {
				// AE: Potential missing data if line contains some text, but we still hit EOF
				break
			}

			if err != nil {
				resChan <- SlicerExecWriteResult{
					Error: fmt.Sprintf("failed to read response: %v", err),
				}
				return
			}

			var result SlicerExecWriteResult
			if err := json.Unmarshal(line, &result); err != nil {
				resChan <- SlicerExecWriteResult{
					Error: fmt.Sprintf("failed to decode response: %v", err),
				}
				return
			}

			if result.Error != "" {
				resChan <- SlicerExecWriteResult{
					Error:  fmt.Sprintf("failed to execute command: %s", result.Error),
					Stdout: result.Stdout,
					Stderr: result.Stderr,
				}
				return
			}

			if result.ExitCode != 0 {
				resChan <- SlicerExecWriteResult{
					Error:  fmt.Sprintf("failed to execute command: %d", result.ExitCode),
					Stdout: result.Stdout,
					Stderr: result.Stderr,
				}
				return
			}

			resChan <- result
		}

	}()

	return resChan, nil
}

// CpToVM copies files from a local path to a VM path.
// The localPath can be a file or directory. The tar stream is created
// internally and sent to the VM.
// uid and gid specify the ownership for extracted files (0 means use default).
func (c *SlicerClient) CpToVM(ctx context.Context, vmName, localPath, vmPath string, uid, gid uint32, permissions, mode string) error {
	// Get absolute path to handle symlinks correctly
	absSrc, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Check if source exists
	if _, err := os.Stat(absSrc); err != nil {
		return fmt.Errorf("source does not exist: %w", err)
	}

	switch mode {
	default:
		return fmt.Errorf("invalid mode: %s", mode)
	case "tar":
		if err := copyToVMTar(ctx, c, absSrc, vmName, vmPath, uid, gid, permissions); err != nil {
			return err
		}
	case "binary":
		if err := copyToVMBinary(ctx, c, absSrc, vmName, vmPath, uid, gid, permissions); err != nil {
			return err
		}
	}

	return nil
}

// CpFromVM copies files from a VM path to a local path.
// The tar stream is received from the VM and extracted to localPath
// with proper renaming logic (supports renaming files/directories).
// If uid or gid are 0, the current user's UID/GID will be used.
// On Windows, chown operations are skipped (uid/gid are ignored).
func (c *SlicerClient) CpFromVM(ctx context.Context, vmName, vmPath, localPath string, uid, gid uint32, permissions, mode string) error {

	switch mode {
	default:
		return fmt.Errorf("invalid mode: %s", mode)
	case "tar":
		return copyFromVMTar(ctx, c, vmName, vmPath, localPath, uid, gid, permissions)
	case "binary":
		return copyFromVMBinary(ctx, c, vmName, vmPath, localPath, uid, gid, permissions)
	}

}

// GetVMStats fetches stats for all VMs or a specific VM if hostname is provided.
// If hostname is empty, returns stats for all VMs.
func (c *SlicerClient) GetVMStats(ctx context.Context, hostname string) ([]SlicerNodeStat, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	if hostname != "" {
		u.Path = fmt.Sprintf("/node/%s/stats", hostname)
	} else {
		u.Path = "/nodes/stats"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to perform GET request: %w", err)
	}
	defer res.Body.Close()

	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var stats []SlicerNodeStat
	if err := json.NewDecoder(bytes.NewBuffer(body)).Decode(&stats); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return stats, nil
}

// GetVMLogs fetches logs for a specific VM
func (c *SlicerClient) GetVMLogs(ctx context.Context, hostname string, lines int) (*SlicerLogsResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/vm/%s/logs", hostname)
	if lines >= 0 {
		q := url.Values{}
		q.Set("lines", strconv.Itoa(lines))
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch logs: %w", err)
	}
	defer res.Body.Close()

	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var logsRes SlicerLogsResponse
	if err := json.NewDecoder(bytes.NewBuffer(body)).Decode(&logsRes); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &logsRes, nil
}

// ListVMs fetches all VMs (nodes)
func (c *SlicerClient) ListVMs(ctx context.Context) ([]SlicerNode, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = "/nodes"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch VMs: %w", err)
	}
	defer res.Body.Close()

	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var nodes []SlicerNode
	if err := json.NewDecoder(bytes.NewBuffer(body)).Decode(&nodes); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return nodes, nil
}

// DeleteVM deletes a VM from a host group
func (c *SlicerClient) DeleteVM(ctx context.Context, groupName, hostname string) (*SlicerDeleteResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/hostgroup/%s/nodes/%s", groupName, hostname)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to delete VM: %w", err)
	}
	defer res.Body.Close()

	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var delResp SlicerDeleteResponse
	if err := json.NewDecoder(bytes.NewBuffer(body)).Decode(&delResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if delResp.Error != "" {
		return nil, fmt.Errorf("%s", delResp.Error)
	}

	return &delResp, nil
}

// CreateVM creates a new VM in a host group
func (c *SlicerClient) CreateVM(ctx context.Context, groupName string, request SlicerCreateVMRequest) (*SlicerCreateNodeResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API URL: %w", err)
	}

	u.Path = fmt.Sprintf("/hostgroup/%s/nodes", groupName)

	jsonBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}
	defer res.Body.Close()

	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
	}

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("status %s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var created SlicerCreateNodeResponse
	if err := json.NewDecoder(bytes.NewBuffer(body)).Decode(&created); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &created, nil
}

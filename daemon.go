package slicer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ShutdownDaemon requests shutdown of the selected daemon as if it received
// SIGTERM. It returns once shutdown is accepted, before VM cleanup completes.
// A supervisor may restart the daemon according to its restart policy.
func (c *SlicerClient) ShutdownDaemon(ctx context.Context) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("failed to parse API URL: %w", err)
	}
	u.Path = "/daemon/shutdown"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to shutdown daemon: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("failed to read daemon shutdown response: %w", err)
	}
	if res.StatusCode != http.StatusAccepted {
		return fmt.Errorf("status %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

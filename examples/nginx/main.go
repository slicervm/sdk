// nginx port-forward example for the Slicer Go SDK.
//
// Flow:
//  1. Create a VM with userdata that apt-installs nginx and starts it. Block
//     server-side via wait=userdata so the daemon's long-poll hands back a
//     ready-to-serve VM.
//  2. Open a host-side port-forward: 127.0.0.1:8080 → VM 127.0.0.1:80 via
//     the slicervm/sdk/forward subpackage.
//  3. Fetch the welcome page from the host through the forward and assert
//     the bytes look right.
//  4. Tear down the forward and delete the VM.
//
// Usage:
//
//	SLICER_URL=~/slicer-mac/slicer.sock go run .
//	SLICER_URL=https://slicer.example.com SLICER_TOKEN=... go run .
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
	"github.com/slicervm/sdk/forward"
)

const userdata = `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qy
apt-get install -qy nginx
systemctl enable --now nginx
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	totalStart := time.Now()
	baseURL := resolveBaseURL()
	token := os.Getenv("SLICER_TOKEN")
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "sbox")

	if token == "" && !isUnixSocket(baseURL) {
		return fmt.Errorf("SLICER_TOKEN is required when SLICER_URL is not a unix socket")
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-nginx-example/1.0", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Printf("→ creating VM in %q with nginx userdata (blocking on wait=userdata)…\n", hostGroup)
	createStart := time.Now()
	node, err := client.CreateVMWithOptions(ctx, hostGroup,
		slicer.SlicerCreateNodeRequest{
			RamBytes: slicer.GiB(1),
			CPUs:     1,
			Userdata: userdata,
			Tags:     []string{"nginx-example"},
		},
		slicer.SlicerCreateNodeOptions{
			Wait:    slicer.SlicerCreateNodeWaitUserdata,
			Timeout: 5 * time.Minute,
		},
	)
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}
	fmt.Printf("  VM %s (%s) ready in %s\n", node.Hostname, node.IP, ms(time.Since(createStart)))

	defer func() {
		fmt.Printf("→ deleting VM %s…\n", node.Hostname)
		if err := client.DeleteNode(hostGroup, node.Hostname); err != nil {
			fmt.Fprintf(os.Stderr, "  delete failed: %v\n", err)
		}
	}()

	fmt.Println("→ opening forward 127.0.0.1:8080 → VM:80…")
	fwd, err := forward.Start(ctx, forward.Options{
		BaseURL: baseURL,
		Token:   token,
		VMName:  node.Hostname,
		Specs:   []string{"127.0.0.1:8080:127.0.0.1:80"},
	})
	if err != nil {
		return fmt.Errorf("open forward: %w", err)
	}
	defer fwd.Close()
	for _, ln := range fwd.Listeners() {
		fmt.Printf("  %s → %s\n", ln.Local, ln.Remote)
	}

	fmt.Println("→ GET http://127.0.0.1:8080/")
	res, err := http.Get("http://127.0.0.1:8080/")
	if err != nil {
		return fmt.Errorf("GET through forward: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	fmt.Printf("  status=%d bytes=%d\n", res.StatusCode, len(body))

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", res.StatusCode)
	}
	if !strings.Contains(string(body), "Welcome to nginx") {
		return fmt.Errorf("response body did not contain \"Welcome to nginx\"")
	}
	fmt.Println("  ✓ welcome page served end-to-end via port-forward")

	fmt.Printf("done in %s\n", ms(time.Since(totalStart)))
	return nil
}

func resolveBaseURL() string {
	baseURL := envOrDefault("SLICER_URL", "~/slicer-mac/slicer.sock")
	if strings.HasPrefix(baseURL, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			baseURL = filepath.Join(home, baseURL[2:])
		}
	}
	return baseURL
}

func isUnixSocket(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "unix://")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func ms(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

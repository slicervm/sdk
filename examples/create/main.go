package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
)

func main() {
	baseURL := os.Getenv("SLICER_URL")
	token := os.Getenv("SLICER_TOKEN")
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "vm")
	tag := envOrDefault("SLICER_CREATE_TAG", fmt.Sprintf("example=create-%d", time.Now().Unix()))

	if baseURL == "" {
		fmt.Println("SLICER_URL is required")
		os.Exit(1)
	}

	if token == "" && !isUnixSocket(baseURL) {
		fmt.Println("SLICER_TOKEN is required")
		os.Exit(1)
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-sdk-go-example/1.0", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	req := slicer.SlicerCreateNodeRequest{
		Tags: []string{tag},
	}

	resp, err := client.CreateVMWithOptions(ctx, hostGroup, req, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitAgent,
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		fmt.Printf("create vm failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created ready VM: hostname=%s ip=%s tag=%s created_at=%s arch=%s\n", resp.Hostname, resp.IP, tag, resp.CreatedAt.Format(time.RFC3339), resp.Arch)
}

func isUnixSocket(baseURL string) bool {
	return strings.HasPrefix(baseURL, "/") || strings.HasPrefix(baseURL, "./") || strings.HasPrefix(baseURL, "~/") || strings.HasPrefix(baseURL, "unix://")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

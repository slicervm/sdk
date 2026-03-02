package main

import (
	"context"
	"fmt"
	"os"
	"time"

	slicer "github.com/slicervm/sdk"
)

func main() {
	baseURL := os.Getenv("SLICER_URL")
	token := os.Getenv("SLICER_TOKEN")

	if baseURL == "" {
		fmt.Println("SLICER_URL is required")
		os.Exit(1)
	}

	if token == "" {
		fmt.Println("SLICER_TOKEN is required")
		os.Exit(1)
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-sdk-go-example/1.0", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req := slicer.SlicerCreateNodeRequest{}

	resp, err := client.CreateVM(ctx, "vm", req)
	if err != nil {
		fmt.Printf("create vm failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created VM: hostname=%s ip=%s created_at=%s arch=%s\n", resp.Hostname, resp.IP, resp.CreatedAt.Format(time.RFC3339), resp.Arch)
}

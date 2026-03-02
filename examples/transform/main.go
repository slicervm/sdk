package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
)

const sampleInput = `hello from host
this file will be copied into the VM
then transformed to upper-case
`

func main() {
	baseURL := envOrDefault("SLICER_URL", "http://192.168.1.34:8080")
	token := os.Getenv("SLICER_TOKEN")
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "vm")
	tag := envOrDefault("FILE_TRANSFER_TAG", fmt.Sprintf("file-transfer-%d", time.Now().Unix()))
	inputContent := envOrDefault("FILE_TRANSFER_INPUT", sampleInput)
	outputName := envOrDefault("FILE_TRANSFER_OUTPUT", "processed.txt")

	if token == "" {
		fmt.Println("SLICER_TOKEN is required")
		os.Exit(1)
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-file-transfer/1.0", nil)

	createCtx, createCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer createCancel()

	node, err := client.CreateVM(createCtx, hostGroup, slicer.SlicerCreateNodeRequest{
		Tags: []string{tag},
	})
	if err != nil {
		fmt.Printf("create VM failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("created VM: hostname=%s ip=%s tag=%s\n", node.Hostname, node.IP, tag)

	execCtx, execCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer execCancel()

	if err := waitForVMReady(execCtx, client, node.Hostname); err != nil {
		fmt.Printf("VM not ready yet: %v\n", err)
		os.Exit(1)
	}

	localInput := "input-" + node.Hostname + ".txt"
	if err := os.WriteFile(localInput, []byte(inputContent), 0o600); err != nil {
		fmt.Printf("write local input failed: %v\n", err)
		os.Exit(1)
	}

	if err := client.CpToVM(execCtx, node.Hostname, localInput, "/home/ubuntu/input.txt", 1000, 1000, "600", "binary"); err != nil {
		fmt.Printf("copy input to VM failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("copied local file to VM: %s -> /home/ubuntu/input.txt\n", localInput)

	out, err := runFileTransform(execCtx, client, node.Hostname)
	if err != nil {
		fmt.Printf("transform command failed: %v\n", err)
		if strings.TrimSpace(out) != "" {
			fmt.Printf("transform output:\n%s\n", strings.TrimSpace(out))
		}
		os.Exit(1)
	}

	localOutput := "output-" + node.Hostname + ".txt"
	if err := client.CpFromVM(execCtx, node.Hostname, "/home/ubuntu/output.txt", localOutput, "600", "binary"); err != nil {
		fmt.Printf("copy output from VM failed: %v\n", err)
		os.Exit(1)
	}

	result, err := os.ReadFile(localOutput)
	if err != nil {
		fmt.Printf("read local output failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("transform output copied to: ./%s\n", localOutput)
	fmt.Printf("content:\n%s", string(result))

	if outputName != "" && outputName != localOutput {
		if err := os.WriteFile(outputName, result, 0o600); err != nil {
			fmt.Printf("rename output failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("also written to: ./%s\n", outputName)
	}
}

func waitForVMReady(ctx context.Context, client *slicer.SlicerClient, nodeName string) error {
	retryDelay := 10 * time.Millisecond
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if _, err := client.GetAgentHealth(ctx, nodeName, false); err == nil {
			return nil
		}

		if attempt%5 == 0 {
			fmt.Printf("attempt %d: VM not ready yet\n", attempt)
		}

		select {
		case <-time.After(retryDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runFileTransform(ctx context.Context, client *slicer.SlicerClient, nodeName string) (string, error) {
	// Upper-case transform to demonstrate processing on the VM.
	cmd := client.CommandContext(ctx, nodeName, "bash", "-lc",
		"tr '[:lower:]' '[:upper:]' < /home/ubuntu/input.txt > /home/ubuntu/output.txt")
	cmd.UID = 1000
	cmd.GID = 1000

	stdout, err := cmd.Output()
	if err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return string(stdout) + string(exitErr.Stderr), err
		}
		return string(stdout), err
	}

	if len(strings.TrimSpace(string(stdout))) == 0 {
		return "", nil
	}

	return string(stdout), nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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
	baseURL := strings.TrimSpace(os.Getenv("SLICER_URL"))
	if baseURL == "" {
		fmt.Println("SLICER_URL is required")
		os.Exit(1)
	}

	token := strings.TrimSpace(os.Getenv("SLICER_TOKEN"))
	hostGroup := strings.TrimSpace(os.Getenv("SLICER_HOST_GROUP"))
	if hostGroup == "" {
		hostGroup = "api"
	}

	waitMode := strings.TrimSpace(os.Getenv("SLICER_CREATE_WAIT"))
	if waitMode == "" {
		waitMode = string(slicer.SlicerCreateNodeWaitAgent)
	}

	timeout := 3 * time.Minute
	if timeoutRaw := strings.TrimSpace(os.Getenv("SLICER_CREATE_TIMEOUT")); timeoutRaw != "" {
		if parsed, err := time.ParseDuration(timeoutRaw); err == nil && parsed > 0 {
			timeout = parsed
		} else if err != nil {
			fmt.Printf("invalid SLICER_CREATE_TIMEOUT: %v\n", err)
			os.Exit(1)
		}
	}

	client := slicer.NewSlicerClient(baseURL, token, "time-till-interactive/1.0", nil)

	createCtx, createCancel := context.WithTimeout(context.Background(), timeout+time.Minute)
	defer createCancel()

	createStart := time.Now()
	node, err := client.CreateVMWithOptions(
		createCtx,
		hostGroup,
		slicer.SlicerCreateNodeRequest{},
		slicer.SlicerCreateNodeOptions{
			Wait:    parseWaitMode(waitMode),
			Timeout: timeout,
		},
	)
	if err != nil {
		fmt.Printf("create failed: %v\n", err)
		os.Exit(1)
	}
	tti := time.Since(createStart)
	fmt.Printf("created vm=%s in=%s\n", node.Hostname, tti)

	execCtx, execCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer execCancel()
	interactiveStart := time.Now()
	execResult, err := client.ExecBuffered(execCtx, node.Hostname, slicer.SlicerExecRequest{
		Command: "echo",
		Args:    []string{"ready"},
		Shell:   "",
	})
	execLatency := time.Since(interactiveStart)
	if err != nil {
		fmt.Printf("interactive exec failed: %v\n", err)
		_, _ = client.DeleteVM(context.Background(), hostGroup, node.Hostname)
		os.Exit(1)
	}
	if execResult.ExitCode != 0 {
		fmt.Printf("interactive exec failed: exit=%d err=%s\n", execResult.ExitCode, execResult.Error)
		_, _ = client.DeleteVM(context.Background(), hostGroup, node.Hostname)
		os.Exit(1)
	}

	fmt.Printf("tti=%s\n", tti)
	fmt.Printf("exec_latency=%s\n", execLatency)
	fmt.Printf("create_to_ready=%s output=%q\n", tti, strings.TrimSpace(execResult.Stdout))

	if err := deleteWithContext(context.Background(), client, hostGroup, node.Hostname); err != nil {
		fmt.Printf("delete failed for %s: %v\n", node.Hostname, err)
		os.Exit(1)
	}

	fmt.Printf("deleted vm=%s\n", node.Hostname)
}

func parseWaitMode(mode string) slicer.SlicerCreateNodeWaitFor {
	switch mode {
	case string(slicer.SlicerCreateNodeWaitUserdata):
		return slicer.SlicerCreateNodeWaitUserdata
	default:
		return slicer.SlicerCreateNodeWaitAgent
	}
}

func deleteWithContext(ctx context.Context, client *slicer.SlicerClient, hostGroup, hostname string) error {
	_, err := client.DeleteVM(ctx, hostGroup, hostname)
	return err
}

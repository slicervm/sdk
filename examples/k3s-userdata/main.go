package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
)

const setupUserdata = `#!/bin/bash
set -euo pipefail

arkade get k3sup kubectl --path /usr/local/bin
chmod +x /usr/local/bin/*

if [ -x /usr/local/bin/k3sup ]; then
  export PATH="/usr/local/bin:${PATH}"
fi

k3sup install --local
mkdir -p /home/ubuntu/.kube
cp kubeconfig /home/ubuntu/.kube/config
chown -R ubuntu:ubuntu /home/ubuntu/

k3sup ready --kubeconfig ./kubeconfig
`

func main() {
	totalStart := time.Now()
	baseURL := envOrDefault("SLICER_URL", "http://192.168.1.34:8080")
	token := os.Getenv("SLICER_TOKEN")
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "vm")
	tag := envOrDefault("K3S_TAG", fmt.Sprintf("k3s-%d", time.Now().Unix()))

	if token == "" {
		fmt.Println("SLICER_TOKEN is required")
		os.Exit(1)
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-k3s-userdata/1.0", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	createStart := time.Now()
	node, err := client.CreateVM(ctx, hostGroup, slicer.SlicerCreateNodeRequest{
		Userdata: setupUserdata,
		Tags:     []string{tag},
	})
	if err != nil {
		fmt.Printf("create VM failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("phase=create_vm_ms elapsed=%d\n", time.Since(createStart).Milliseconds())

	nodeIP, err := parseNodeIP(node.IP)
	if err != nil {
		fmt.Printf("warning: could not parse VM ip (%s): %v\n", node.IP, err)
	}

	fmt.Printf("created VM: hostname=%s ip=%s tag=%s\n", node.Hostname, node.IP, tag)

	execCtx, execCancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer execCancel()

	readyStart := time.Now()
	err = waitForVMReady(execCtx, client, node.Hostname)
	if err != nil {
		fmt.Printf("VM not ready yet: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("phase=wait_agent_ready_ms elapsed=%d\n", time.Since(readyStart).Milliseconds())

	kubeStart := time.Now()
	out, err := waitForKubectlNodes(execCtx, client, node.Hostname, 1000)
	if err != nil {
		fmt.Printf("kubectl check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("phase=kubectl_get_nodes_ms elapsed=%d\n", time.Since(kubeStart).Milliseconds())

	fmt.Printf("kubectl get nodes output:\n%s\n", strings.TrimSpace(out))

	if nodeIP == "" {
		nodeIP = envOrDefault("SLICER_NODE_IP", "")
	}

	if nodeIP == "" {
		fmt.Printf("warning: no IP available for kubeconfig rewrite\n")
	} else {
		kubeCopyStart := time.Now()
		localConfig, err := copyAndRewriteKubeconfig(execCtx, client, node.Hostname, nodeIP)
		if err != nil {
			fmt.Printf("kubeconfig copy failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("phase=copy_kubeconfig_ms elapsed=%d\n", time.Since(kubeCopyStart).Milliseconds())
		fmt.Printf("kubeconfig saved and updated for direct use: %s\n", localConfig)
		fmt.Printf("try it now:\n")
		fmt.Printf("KUBECONFIG=%s kubectl get nodes\n", localConfig)
	}

	fmt.Printf("phase=total_ms elapsed=%d\n", time.Since(totalStart).Milliseconds())
}

func waitForKubectlNodes(ctx context.Context, client *slicer.SlicerClient, nodeName string, uid uint32) (string, error) {
	retryDelay := 15 * time.Second
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		out, err := runKubectlNodes(ctx, client, nodeName, uid)
		if err == nil {
			return out, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return out, err
		}

		fmt.Printf("attempt %d: kubectl not ready yet (%v)\n", attempt, err)

		select {
		case <-time.After(retryDelay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func waitForVMReady(ctx context.Context, client *slicer.SlicerClient, nodeName string) error {
	retryDelay := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, err := client.GetAgentHealth(ctx, nodeName, false)
		if err == nil {
			return nil
		}

		fmt.Printf("attempt %d: VM not ready yet (%v)\n", attempt, err)

		select {
		case <-time.After(retryDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func runKubectlNodes(ctx context.Context, client *slicer.SlicerClient, nodeName string, uid uint32) (string, error) {
	cmd := client.CommandContext(ctx, nodeName, "kubectl", "get", "nodes")
	cmd.UID = uid
	cmd.GID = uid

	stdout, err := cmd.Output()
	if err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return string(stdout) + string(exitErr.Stderr), err
		}
		return string(stdout), err
	}

	text := string(stdout)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("no output from kubectl get nodes")
	}

	return text, nil
}

func copyAndRewriteKubeconfig(ctx context.Context, client *slicer.SlicerClient, nodeName, nodeIP string) (string, error) {
	if strings.TrimSpace(nodeName) == "" {
		return "", fmt.Errorf("empty node name")
	}
	if strings.TrimSpace(nodeIP) == "" {
		return "", fmt.Errorf("empty node ip")
	}

	localFile := "./kubeconfig-" + nodeName + ".yaml"

	if err := client.CpFromVM(ctx, nodeName, "/home/ubuntu/.kube/config", localFile, "", "binary"); err != nil {
		return "", fmt.Errorf("cp kubeconfig from VM failed: %w", err)
	}

	raw, err := os.ReadFile(localFile)
	if err != nil {
		return "", fmt.Errorf("read local kubeconfig failed: %w", err)
	}

	rewritten := strings.ReplaceAll(string(raw), "127.0.0.1", nodeIP)
	rewritten = strings.ReplaceAll(rewritten, "localhost", nodeIP)

	if err := os.WriteFile(localFile, []byte(rewritten), 0o600); err != nil {
		return "", fmt.Errorf("write updated kubeconfig failed: %w", err)
	}

	return localFile, nil
}

func parseNodeIP(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("empty ip")
	}

	if strings.Contains(trimmed, "/") {
		ip, _, err := net.ParseCIDR(trimmed)
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}

	ip := net.ParseIP(trimmed)
	if ip == nil {
		return "", fmt.Errorf("invalid ip")
	}

	return ip.String(), nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

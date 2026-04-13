package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

k3sup ready --kubeconfig ./kubeconfig --pause 500ms --attempts 120
`

func main() {
	totalStart := time.Now()
	baseURL := resolveBaseURL() // Override via SLICER_URL if needed.
	token := os.Getenv("SLICER_TOKEN")
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "vm")
	tag := envOrDefault("K3S_TAG", fmt.Sprintf("example=k3s-%d", time.Now().Unix()))

	if token == "" && !isUnixSocket(baseURL) {
		fmt.Println("SLICER_TOKEN is required")
		os.Exit(1)
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-k3s-userdata/1.0", nil)

	infoCtx, infoCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer infoCancel()

	hostGroup, err := resolveHostGroup(infoCtx, client, hostGroup)
	if err != nil {
		fmt.Printf("failed to resolve hostgroup from /info: %v\n", err)
		fmt.Printf("using configured host group: %s\n", hostGroup)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	createStart := time.Now()
	node, err := client.CreateVMWithOptions(ctx, hostGroup, slicer.SlicerCreateNodeRequest{
		Userdata: setupUserdata,
		Tags:     []string{tag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: 12 * time.Minute,
	})
	if err != nil {
		fmt.Printf("create VM failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("phase=create_ready_vm_ms elapsed=%d\n", time.Since(createStart).Milliseconds())

	nodeIP, err := parseNodeIP(node.IP)
	if err != nil {
		fmt.Printf("warning: could not parse VM ip (%s): %v\n", node.IP, err)
	}

	fmt.Printf("created ready VM: hostname=%s ip=%s tag=%s\n", node.Hostname, node.IP, tag)

	execCtx, execCancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer execCancel()

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
	retryDelay := 1 * time.Second
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
		return "", fmt.Errorf("no output from kubectl command")
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

func isUnixSocket(baseURL string) bool {
	return strings.HasPrefix(baseURL, "/") || strings.HasPrefix(baseURL, "./")
}

func resolveBaseURL() string {
	baseURL := envOrDefault("SLICER_URL", "~/slicer-mac/slicer.sock")
	if strings.HasPrefix(baseURL, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Printf("resolve SLICER_URL home directory: %v\n", err)
			os.Exit(1)
		}
		baseURL = filepath.Join(home, baseURL[2:])
	}
	return baseURL
}

func resolveHostGroup(ctx context.Context, client *slicer.SlicerClient, configured string) (string, error) {
	info, err := client.GetInfo(ctx)
	if err != nil {
		return configured, err
	}

	if strings.EqualFold(info.Platform, "darwin") {
		return "sbox", nil
	}

	return configured, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

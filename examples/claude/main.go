package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
)

const setupUserdata = `#!/bin/bash
set -euo pipefail

arkade get claude --path /usr/local/bin
chmod 755 /usr/local/bin/claude
`

func main() {
	baseURL := envOrDefault("SLICER_URL", "http://192.168.1.34:8080")
	token := os.Getenv("SLICER_TOKEN")
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "vm")
	tag := envOrDefault("CLAUDE_TAG", fmt.Sprintf("example=claude-%d", time.Now().Unix()))

	if token == "" {
		fmt.Println("SLICER_TOKEN is required")
		os.Exit(1)
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-claude-example/1.0", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	node, err := client.CreateVMWithOptions(ctx, hostGroup, slicer.SlicerCreateNodeRequest{
		Userdata: setupUserdata,
		Tags:     []string{tag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		fmt.Printf("create VM failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("created ready VM: hostname=%s ip=%s tag=%s\n", node.Hostname, node.IP, tag)

	execCtx, execCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer execCancel()

	if err := copyClaudeCredentials(execCtx, client, node.Hostname); err != nil {
		fmt.Printf("copy claude credentials failed: %v\n", err)
		os.Exit(1)
	}

	if err := ensureClaudeExecutable(execCtx, client, node.Hostname); err != nil {
		fmt.Printf("prepare claude executable failed: %v\n", err)
		os.Exit(1)
	}

	out, err := runClaude(execCtx, client, node.Hostname)
	if err != nil {
		fmt.Printf("claude command failed: %v\n", err)
		if strings.TrimSpace(out) != "" {
			fmt.Printf("claude output:\n%s\n", strings.TrimSpace(out))
		}
		os.Exit(1)
	}

	fmt.Printf("claude output:\n%s\n", strings.TrimSpace(out))
}

func runClaude(ctx context.Context, client *slicer.SlicerClient, nodeName string) (string, error) {
	cmd := client.CommandContext(ctx, nodeName, "/usr/local/bin/claude", "-p", "What's my username?")
	cmd.UID = 1000
	cmd.GID = 1000
	cmd.Dir = "/home/ubuntu"
	cmd.Env = append(cmd.Env, "HOME=/home/ubuntu")

	stdout, err := cmd.Output()
	if err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return string(stdout) + string(exitErr.Stderr), err
		}
		return string(stdout), err
	}

	if len(strings.TrimSpace(string(stdout))) == 0 {
		return "", fmt.Errorf("no output from claude command")
	}

	return string(stdout), nil
}

func ensureClaudeExecutable(ctx context.Context, client *slicer.SlicerClient, nodeName string) error {
	cmd := client.CommandContext(ctx, nodeName, "chmod", "+x", "/usr/local/bin/claude")
	if _, err := cmd.Output(); err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("chmod failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("chmod failed: %w", err)
	}

	return nil
}

func copyClaudeCredentials(ctx context.Context, client *slicer.SlicerClient, nodeName string) error {
	credentialPath, err := saveClaudeCredentials()
	if err != nil {
		return err
	}
	defer os.Remove(credentialPath)

	if err := prepareClaudeDir(ctx, client, nodeName); err != nil {
		return err
	}

	if err := client.CpToVM(ctx, nodeName, credentialPath, "/home/ubuntu/.claude/.credentials.json", 1000, 1000, "600", "binary"); err != nil {
		return fmt.Errorf("copy credentials failed: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("unable to determine home directory: %w", err)
	}

	if err := copyOptionalClaudeFile(ctx, client, nodeName, filepath.Join(home, ".claude", "settings.json"), "/home/ubuntu/.claude/settings.json", 1000, 1000); err != nil {
		return err
	}

	if err := copyOptionalClaudeFile(ctx, client, nodeName, filepath.Join(home, ".claude.json"), "/home/ubuntu/.claude.json", 1000, 1000); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(home, ".claude.json")); errors.Is(err, os.ErrNotExist) {
		if err := writeDefaultClaudePrefs(ctx, client, nodeName, 1000, 1000); err != nil {
			return err
		}
	}

	return nil
}

func copyOptionalClaudeFile(ctx context.Context, client *slicer.SlicerClient, nodeName, localPath, remotePath string, uid, gid uint32) error {
	if _, err := os.Stat(localPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to inspect local claude file %s: %w", localPath, err)
	}

	if err := client.CpToVM(ctx, nodeName, localPath, remotePath, uid, gid, "600", "binary"); err != nil {
		return fmt.Errorf("copy optional claude file %s failed: %w", filepath.Base(localPath), err)
	}

	return nil
}

func writeDefaultClaudePrefs(ctx context.Context, client *slicer.SlicerClient, nodeName string, uid, gid uint32) error {
	prefs := "{\n  \"bypassPermissionsModeAccepted\": true\n}\n"
	tmp, err := os.CreateTemp("", "claude-prefs-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temporary claude prefs file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := io.WriteString(tmp, prefs); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write temporary claude prefs file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to set temporary claude prefs permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary claude prefs file: %w", err)
	}

	if err := client.CpToVM(ctx, nodeName, tmp.Name(), "/home/ubuntu/.claude.json", uid, gid, "600", "binary"); err != nil {
		return fmt.Errorf("write default claude prefs failed: %w", err)
	}

	return nil
}

func saveClaudeCredentials() (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	raw, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = exitErr.Stderr
		}
		return "", fmt.Errorf("failed to read macOS credential: %w; stderr=%s", err, strings.TrimSpace(string(stderr)))
	}

	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", fmt.Errorf("empty Claude credentials")
	}

	tmp, err := os.CreateTemp("", "claude-credentials-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary credential file: %w", err)
	}

	if _, err := io.WriteString(tmp, text); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to write credentials to temporary file: %w", err)
	}

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to set temporary credentials permissions: %w", err)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to close temporary credential file: %w", err)
	}

	return tmp.Name(), nil
}

func prepareClaudeDir(ctx context.Context, client *slicer.SlicerClient, nodeName string) error {
	cmd := client.CommandContext(ctx, nodeName, "mkdir", "-p", "/home/ubuntu/.claude")
	if _, err := cmd.Output(); err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("prepare claude directory failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("prepare claude directory failed: %w", err)
	}

	chownCmd := client.CommandContext(ctx, nodeName, "chown", "-R", "1000:1000", "/home/ubuntu/.claude")
	if _, err := chownCmd.Output(); err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("prepare claude directory ownership failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("prepare claude directory ownership failed: %w", err)
	}

	chmodCmd := client.CommandContext(ctx, nodeName, "chmod", "700", "/home/ubuntu/.claude")
	if _, err := chmodCmd.Output(); err != nil {
		if exitErr := new(slicer.ExitError); errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("prepare claude directory permissions failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("prepare claude directory permissions failed: %w", err)
	}

	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

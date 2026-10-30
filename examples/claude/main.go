package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
)

const setupUserdata = `#!/bin/bash
set -euo pipefail

arkade get claude --path /usr/local/bin
chmod 0755 /usr/local/bin/claude
`

const guestWorkspace = "/home/ubuntu/workspace"

type options struct {
	baseURL           string
	token             string
	tokenFile         string
	tokenExplicit     bool
	tokenFileExplicit bool
	hostGroup         string
	credentials       string
	settings          string
	prompt            string
	background        bool
	keep              bool
	timeout           time.Duration
	cpus              int
	ramGB             int64
	keychainLookup    func(context.Context) ([]byte, error)
}

func main() {
	opts := options{}
	flag.StringVar(&opts.baseURL, "url", os.Getenv("SLICER_URL"), "Slicer API URL or Unix socket path")
	flag.StringVar(&opts.token, "token", os.Getenv("SLICER_TOKEN"), "Slicer API token")
	flag.StringVar(&opts.tokenFile, "token-file", os.Getenv("SLICER_TOKEN_FILE"), "file containing the Slicer API token")
	flag.StringVar(&opts.hostGroup, "hostgroup", os.Getenv("SLICER_HOST_GROUP"), "host group for the VM (auto-selects sbox or the only group)")
	flag.StringVar(&opts.credentials, "credentials-file", os.Getenv("CLAUDE_CREDENTIALS_FILE"), "Claude credentials file (defaults to ~/.claude/.credentials.json or the macOS Keychain)")
	flag.StringVar(&opts.settings, "settings-file", os.Getenv("CLAUDE_SETTINGS_FILE"), "optional Claude settings file to copy into the VM")
	flag.StringVar(&opts.prompt, "prompt", envOrDefault("CLAUDE_PROMPT", "What is my username? Reply with the username only."), "prompt to send to Claude")
	flag.BoolVar(&opts.background, "background", false, "start a background Claude session that can be attached to later")
	flag.BoolVar(&opts.keep, "keep", false, "keep the VM after the command finishes")
	flag.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "overall timeout for provisioning and inference")
	flag.IntVar(&opts.cpus, "cpus", 2, "VM CPU count")
	flag.Int64Var(&opts.ramGB, "ram-gb", 4, "VM RAM in GiB")
	flag.Parse()
	flag.Visit(func(value *flag.Flag) {
		switch value.Name {
		case "token":
			opts.tokenExplicit = true
		case "token-file":
			opts.tokenFileExplicit = true
		}
	})

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if strings.TrimSpace(opts.baseURL) == "" {
		return fmt.Errorf("--url or SLICER_URL is required")
	}
	if strings.TrimSpace(opts.prompt) == "" {
		return fmt.Errorf("--prompt must not be empty")
	}
	if opts.cpus < 1 || opts.ramGB < 1 {
		return fmt.Errorf("--cpus and --ram-gb must be greater than zero")
	}
	if opts.timeout <= 0 {
		return fmt.Errorf("--timeout must be greater than zero")
	}

	baseURL := expandPath(opts.baseURL)
	if opts.tokenExplicit && opts.tokenFileExplicit {
		return fmt.Errorf("--token and --token-file cannot be used together")
	}
	if opts.tokenFileExplicit && strings.TrimSpace(opts.tokenFile) == "" {
		return fmt.Errorf("--token-file must not be empty")
	}
	token, err := resolveToken(opts.token, opts.tokenFile, opts.tokenFileExplicit)
	if err != nil {
		return err
	}

	credentials, cleanupCredentials, err := resolveClaudeCredentials(opts.credentials, opts.keychainLookup)
	if err != nil {
		return err
	}
	defer cleanupCredentials()

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	client := slicer.NewSlicerClient(baseURL, token, "slicer-claude-example/2.0", nil)
	hostGroup, err := selectHostGroup(ctx, client, opts.hostGroup)
	if err != nil {
		return err
	}

	runTag, err := newRunTag()
	if err != nil {
		return fmt.Errorf("create run tag: %w", err)
	}
	serverWait, err := serverWaitTimeout(ctx)
	if err != nil {
		return err
	}
	keepVM := opts.keep || opts.background
	fmt.Printf("Creating a VM in %q (run=%s)\n", hostGroup, runTag)
	node, err := createVM(ctx, client, hostGroup, runTag, slicer.SlicerCreateNodeRequest{
		CPUs:       opts.cpus,
		RamBytes:   slicer.GiB(opts.ramGB),
		Persistent: keepVM,
		Userdata:   setupUserdata,
		Tags: []string{
			"example=claude",
			"run=" + runTag,
		},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: serverWait,
	})
	if err != nil {
		return err
	}
	if node.HostGroup != "" {
		hostGroup = node.HostGroup
	}
	fmt.Printf("VM %s is ready at %s\n", node.Hostname, node.IP)

	completed := false
	defer func() {
		if opts.keep || (opts.background && completed) {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		fmt.Printf("Deleting VM %s\n", node.Hostname)
		if _, deleteErr := client.DeleteVM(cleanupCtx, hostGroup, node.Hostname); deleteErr != nil {
			fmt.Fprintf(os.Stderr, "warning: delete VM %s: %v\n", node.Hostname, deleteErr)
		}
	}()

	if err := configureClaude(ctx, client, node.Hostname, credentials, opts.settings); err != nil {
		return err
	}

	fmt.Printf("Running Claude in %s\n", node.Hostname)
	claudeArgs := []string{"--dangerously-skip-permissions"}
	if opts.background {
		claudeArgs = append(claudeArgs, "--background")
	} else {
		claudeArgs = append(claudeArgs, "--print")
	}
	claudeArgs = append(claudeArgs, opts.prompt)
	command := client.CommandContext(ctx, node.Hostname, "/usr/local/bin/claude", claudeArgs...)
	command.UID = slicer.NonRootUser
	command.GID = slicer.NonRootUser
	command.Dir = guestWorkspace
	command.Env = []string{
		"HOME=/home/ubuntu",
		"USER=ubuntu",
		"PATH=/home/ubuntu/.local/bin:/home/ubuntu/.arkade/bin:/usr/local/bin:/usr/bin:/bin",
	}

	output, err := command.CombinedOutput()
	if len(output) > 0 {
		fmt.Printf("\n%s\n", strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("run Claude: %w", err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return fmt.Errorf("Claude returned no output")
	}
	completed = true

	if keepVM {
		fmt.Printf("\nVM retained: %s (host group %s)\n", node.Hostname, hostGroup)
	}
	if opts.background {
		fmt.Printf("Attach later with a VM shell, then run `claude agents` and `claude attach <id>`.\n")
	}
	return nil
}

func createVM(ctx context.Context, client *slicer.SlicerClient, hostGroup, runTag string, request slicer.SlicerCreateNodeRequest, createOptions slicer.SlicerCreateNodeOptions) (*slicer.SlicerCreateNodeResponse, error) {
	node, err := client.CreateVMWithOptions(ctx, hostGroup, request, createOptions)
	if err == nil {
		return node, nil
	}

	createErr := fmt.Errorf("create VM: %w", err)
	waitForVM := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
	cleanupTimeout := time.Minute
	if waitForVM {
		cleanupTimeout = 30 * time.Second
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if cleanupErr := deleteVMsByRunTag(cleanupCtx, client, hostGroup, runTag, waitForVM); cleanupErr != nil {
		return nil, errors.Join(createErr, fmt.Errorf("reconcile failed create: %w", cleanupErr))
	}
	return nil, createErr
}

func deleteVMsByRunTag(ctx context.Context, client *slicer.SlicerClient, hostGroup, runTag string, waitForVM bool) error {
	tag := "run=" + runTag
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		nodes, err := client.GetHostGroupNodes(ctx, hostGroup)
		if err != nil {
			return fmt.Errorf("list VMs while looking for tag %s: %w", tag, err)
		}
		found := false
		for _, node := range nodes {
			if !contains(node.Tags, tag) {
				continue
			}
			found = true
			if _, err := client.DeleteVM(ctx, hostGroup, node.Hostname); err != nil {
				return fmt.Errorf("delete VM %s: %w", node.Hostname, err)
			}
		}
		if found || !waitForVM {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("VM tagged %s did not appear before cleanup deadline: %w", tag, ctx.Err())
		case <-ticker.C:
		}
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func serverWaitTimeout(ctx context.Context) (time.Duration, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, fmt.Errorf("provisioning requires a context deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}

	margin := 5 * time.Second
	if remaining < 25*time.Second {
		margin = remaining / 5
	}
	wait := remaining - margin
	if wait <= 0 {
		return 0, fmt.Errorf("timeout is too short for provisioning")
	}
	return wait, nil
}

func configureClaude(ctx context.Context, client *slicer.SlicerClient, vmName, credentials, settings string) error {
	prepare := client.CommandContext(ctx, vmName, "install", "-d", "-m", "0700", "-o", "1000", "-g", "1000", "/home/ubuntu/.claude", guestWorkspace)
	if output, err := prepare.CombinedOutput(); err != nil {
		return commandError("prepare Claude directory", output, err)
	}

	if err := client.CpToVM(ctx, vmName, credentials, "/home/ubuntu/.claude/.credentials.json", 1000, 1000, "0600", "binary"); err != nil {
		return fmt.Errorf("copy Claude credentials: %w", err)
	}

	if settings != "" {
		settings = expandPath(settings)
		if _, err := os.Stat(settings); err != nil {
			return fmt.Errorf("Claude settings %s: %w", settings, err)
		}
		if err := client.CpToVM(ctx, vmName, settings, "/home/ubuntu/.claude/settings.json", 1000, 1000, "0600", "binary"); err != nil {
			return fmt.Errorf("copy Claude settings: %w", err)
		}
	}

	const acceptSandbox = `import json, os
p = os.path.expanduser("~/.claude.json")
try:
    with open(p) as f:
        data = json.load(f)
except FileNotFoundError:
    data = {}
data["bypassPermissionsModeAccepted"] = True
projects = data.setdefault("projects", {})
project = projects.setdefault("/home/ubuntu/workspace", {})
project["hasTrustDialogAccepted"] = True
with open(p, "w") as f:
    json.dump(data, f)
os.chmod(p, 0o600)
`
	prefs := client.CommandContext(ctx, vmName, "python3", "-c", acceptSandbox)
	prefs.UID = slicer.NonRootUser
	prefs.GID = slicer.NonRootUser
	prefs.Env = []string{
		"HOME=/home/ubuntu",
		"USER=ubuntu",
		"PATH=/home/ubuntu/.local/bin:/home/ubuntu/.arkade/bin:/usr/local/bin:/usr/bin:/bin",
	}
	if output, err := prefs.CombinedOutput(); err != nil {
		return commandError("configure Claude permissions", output, err)
	}

	return nil
}

func resolveClaudeCredentials(configured string, keychainLookup func(context.Context) ([]byte, error)) (string, func(), error) {
	if configured != "" {
		path := expandPath(configured)
		if _, err := os.Stat(path); err != nil {
			return "", func() {}, fmt.Errorf("Claude credentials %s: %w", path, err)
		}
		return path, func() {}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", func() {}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	if _, err := os.Stat(path); err == nil {
		return path, func() {}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", func() {}, fmt.Errorf("Claude credentials %s: %w", path, err)
	}

	if keychainLookup == nil {
		if runtime.GOOS != "darwin" {
			return "", func() {}, fmt.Errorf("Claude credentials not found at %s; set --credentials-file", path)
		}
		keychainLookup = lookupClaudeKeychain
	}
	raw, err := keychainLookup(context.Background())
	if err != nil {
		return "", func() {}, fmt.Errorf("read Claude credentials from macOS Keychain: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return "", func() {}, fmt.Errorf("macOS Keychain returned empty Claude credentials")
	}

	tmp, err := os.CreateTemp("", "slicer-claude-credentials-*.json")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary credentials file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("protect temporary credentials file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write temporary credentials file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close temporary credentials file: %w", err)
	}
	return name, cleanup, nil
}

func lookupClaudeKeychain(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "security", "find-generic-password", "-w", "-s", "Claude Code-credentials").Output()
}

func selectHostGroup(ctx context.Context, client *slicer.SlicerClient, requested string) (string, error) {
	groups, err := client.GetHostGroups(ctx)
	if err != nil {
		return "", fmt.Errorf("list host groups: %w", err)
	}
	if requested != "" {
		for _, group := range groups {
			if group.Name == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf("host group %q not found (available: %s)", requested, hostGroupNames(groups))
	}
	for _, group := range groups {
		if group.Name == "sbox" {
			return group.Name, nil
		}
	}
	if len(groups) == 1 {
		return groups[0].Name, nil
	}
	if len(groups) == 0 {
		return "", fmt.Errorf("no host groups configured")
	}
	return "", fmt.Errorf("multiple host groups configured (%s); set --hostgroup", hostGroupNames(groups))
}

func hostGroupNames(groups []slicer.SlicerHostGroup) string {
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.Name)
	}
	return strings.Join(names, ", ")
}

func resolveToken(token, tokenFile string, tokenFileExplicit bool) (string, error) {
	if strings.TrimSpace(token) != "" && !tokenFileExplicit {
		return strings.TrimSpace(token), nil
	}
	if tokenFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(expandPath(tokenFile))
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return strings.TrimSpace(string(raw)), nil
}

func newRunTag() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "claude-" + hex.EncodeToString(raw), nil
}

func commandError(action string, output []byte, err error) error {
	if text := strings.TrimSpace(string(output)); text != "" {
		return fmt.Errorf("%s: %w: %s", action, err, text)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func expandPath(value string) string {
	if strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

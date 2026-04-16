// Background-exec example: detached docker buildx build+push with reattach.
//
// Flow:
//  1. Create a VM with userdata that installs Docker from get.docker.com.
//  2. Clone inlets/mixctl (foreground exec).
//  3. Launch `docker buildx build --push -t ttl.sh/... .` as a *background*
//     exec and immediately detach — do not stream the logs. buildx --push
//     combines build + registry push into one step so the background exec
//     owns the full workflow; the client never has to run a separate push.
//  4. Sleep 5 s, call ExecInfo to prove the work is running while detached,
//     then reattach the log stream from from_id=0 and follow to the exit frame.
//  5. Reap the background entry.
//
// Usage:
//
//	SLICER_URL=http://host:8080 SLICER_TOKEN_FILE=~/.slicer/token go run .
//	SLICER_URL=/path/to/slicer.sock SLICER_TOKEN="" go run .
//
// Env:
//
//	SLICER_URL         required
//	SLICER_TOKEN       auth token (skip if using a unix socket without auth)
//	SLICER_TOKEN_FILE  alternative to SLICER_TOKEN
//	SLICER_HOST_GROUP  host group to create the VM in (default "demo")

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	slicer "github.com/slicervm/sdk"
)

const userdata = `#!/bin/bash
set -euo pipefail
exec > >(tee -a /var/log/slicer-bgexec-userdata.log) 2>&1
echo "phase=userdata_start ts=$(date -Is)"

export DEBIAN_FRONTEND=noninteractive
apt-get update -qy
apt-get install -qy curl ca-certificates git

curl -fsSL https://get.docker.com | sh
systemctl enable --now docker
usermod -aG docker ubuntu

echo "phase=userdata_done ts=$(date -Is)"
`

func main() {
	keep := flag.Bool("keep", false, "keep the VM after completion")
	flag.Parse()

	baseURL := resolveBaseURL()
	token := resolveToken()
	hostGroup := envOrDefault("SLICER_HOST_GROUP", "demo")

	if token == "" && !isUnixSocket(baseURL) {
		log.Fatal("SLICER_TOKEN or SLICER_TOKEN_FILE is required for non-unix-socket transports")
	}

	client := slicer.NewSlicerClient(baseURL, token, "slicer-bgexec-docker-build/1.0", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	log.Printf("=== docker build: detach, reattach after 5s, follow to exit, then push ===")

	tag := fmt.Sprintf("bgexec=docker-%s", shortID())
	node, err := client.CreateVMWithOptions(ctx, hostGroup, slicer.SlicerCreateNodeRequest{
		CPUs:     2,
		RamBytes: slicer.GiB(4),
		Userdata: userdata,
		Tags:     []string{tag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: 15 * time.Minute,
	})
	if err != nil {
		log.Fatalf("create VM: %v", err)
	}
	log.Printf("vm=%s ip=%s", node.Hostname, node.IP)
	if !*keep {
		defer func() {
			log.Printf("deleting vm=%s", node.Hostname)
			if err := client.DeleteNode(hostGroup, node.Hostname); err != nil {
				log.Printf("delete vm failed: %v", err)
			}
		}()
	}

	// Foreground clone.
	log.Printf("cloning inlets/mixctl (foreground)")
	cmd := client.CommandContext(ctx, node.Hostname, "bash", "-lc",
		"rm -rf ~/mixctl && git clone --depth 1 https://github.com/inlets/mixctl ~/mixctl")
	cmd.UID, cmd.GID = 1000, 1000
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("git clone: %v; out=%s", err, string(out))
	}
	log.Printf("clone ok out=%d bytes", len(out))

	// Background docker buildx build --push.
	imageTag := fmt.Sprintf("ttl.sh/slicer-mixctl-%s:1h", shortID())
	log.Printf("docker buildx build --push -> %s (background, 4 MiB ring)", imageTag)
	ex, err := client.ExecBackground(ctx, node.Hostname, slicer.ExecBackgroundRequest{
		Command:   "docker",
		Args:      []string{"buildx", "build", "--push", "-t", imageTag, "."},
		Cwd:       "/home/ubuntu/mixctl",
		UID:       1000,
		GID:       1000,
		RingBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		log.Fatalf("ExecBackground: %v", err)
	}
	log.Printf("exec_id=%s pid=%d ring=%d", ex.ExecID, ex.PID, ex.RingBytes)

	// Detach — do not stream — for 5s.
	log.Printf("detached; sleeping 5s before reattach")
	time.Sleep(5 * time.Second)

	info, err := client.ExecInfo(ctx, node.Hostname, ex.ExecID)
	if err != nil {
		log.Fatalf("ExecInfo mid-run: %v", err)
	}
	log.Printf("mid-run info running=%v bytes_written=%d next_id=%d",
		info.Running, info.BytesWritten, info.NextID)

	// Reattach and follow until exit.
	reCtx, reCancel := context.WithTimeout(ctx, 20*time.Minute)
	defer reCancel()
	frames, err := client.ExecLogs(reCtx, node.Hostname, ex.ExecID,
		slicer.LogOptions{Follow: true, FromID: 0})
	if err != nil {
		log.Fatalf("ExecLogs reattach: %v", err)
	}
	log.Printf("--- docker buildx build --push output (resumed from_id=0) ---")
	var exitCode = -1
	var count int
	for f := range frames {
		count++
		switch f.Type {
		case "gap":
			log.Printf("gap id=%d dropped_frames=%d dropped_bytes=%d: %s",
				f.ID, f.DroppedFrames, f.DroppedBytes, f.Message)
		case "stdout", "stderr":
			_, _ = os.Stdout.WriteString(frameText(f))
		case "exit":
			exitCode = f.ExitCode
			log.Printf("--- end docker buildx build --push output ---")
			log.Printf("exit id=%d exit_code=%d signal=%s", f.ID, f.ExitCode, f.Signal)
		}
		if f.Type == "exit" {
			break
		}
	}
	if exitCode != 0 {
		log.Fatalf("docker buildx build --push failed: exit_code=%d", exitCode)
	}
	log.Printf("buildx build+push completed frames=%d image=%s", count, imageTag)

	if _, err := client.ExecDelete(ctx, node.Hostname, ex.ExecID); err != nil {
		log.Fatalf("ExecDelete: %v", err)
	}
	log.Printf("PASS image=%s", imageTag)
}

// ----- helpers -----------------------------------------------------------

func frameText(f slicer.SlicerExecWriteResult) string {
	if f.Data != "" {
		return f.Data
	}
	if f.Stdout != "" {
		return f.Stdout
	}
	return f.Stderr
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func snippet(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func resolveBaseURL() string {
	baseURL := envOrDefault("SLICER_URL", "http://127.0.0.1:8080")
	if strings.HasPrefix(baseURL, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			baseURL = filepath.Join(home, baseURL[2:])
		}
	}
	return baseURL
}

func resolveToken() string {
	if tok := strings.TrimSpace(os.Getenv("SLICER_TOKEN")); tok != "" {
		return tok
	}
	tf := strings.TrimSpace(os.Getenv("SLICER_TOKEN_FILE"))
	if tf == "" {
		return ""
	}
	if strings.HasPrefix(tf, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			tf = filepath.Join(home, tf[2:])
		}
	}
	b, err := os.ReadFile(tf)
	if err != nil {
		log.Fatalf("read SLICER_TOKEN_FILE %s: %v", tf, err)
	}
	return strings.TrimSpace(string(b))
}

func isUnixSocket(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "unix://") || strings.HasSuffix(s, ".sock")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}


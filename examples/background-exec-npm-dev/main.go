// Background-exec example: long-running dev server with detach + reattach.
//
// Flow:
//  1. Create a VM with userdata that installs Node 20 and scaffolds a tiny
//     Next.js app at /home/ubuntu/app.
//  2. Launch `npm run dev` as a *background* exec — the child is detached and
//     keeps running even after every client disconnects.
//  3. Stream the log ring until the Next.js "Local: http://localhost:3000"
//     marker appears, then close the stream.
//  4. Open a port-forward 127.0.0.1:3000 -> VM:3000 and assert the dev server
//     is serving HTML through it.
//  5. Disconnect for 5 s, reconnect with `from_id=<last_seen_id+1>`, verify
//     more frames arrive after reconnect.
//  6. Probe info(), kill(TERM), wait-exit(), delete(). Confirm info-after-
//     delete returns 410 Gone.
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
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
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
exec > >(tee -a /var/log/slicer-bgexec-userdata.log) 2>&1
echo "phase=userdata_start ts=$(date -Is)"

# Opinionated stack: use arkade (preinstalled in slicer-systemd image) to pull
# a ready-to-run Node/npm into /usr/local. Avoids a full apt update.
arkade system install node
export PATH=/usr/local/bin:$PATH
node --version
npm --version

install -d -o ubuntu -g ubuntu /home/ubuntu/app

cat > /home/ubuntu/app/package.json <<'EOF'
{
  "name": "slicer-bgexec-demo",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "next dev -p 3000 -H 0.0.0.0"
  },
  "dependencies": {
    "next": "14.2.15",
    "react": "18.3.1",
    "react-dom": "18.3.1"
  }
}
EOF

mkdir -p /home/ubuntu/app/pages
cat > /home/ubuntu/app/pages/index.js <<'EOF'
export default function Home() {
  return <main><h1>slicer bg-exec demo</h1><p>alive</p></main>;
}
EOF

chown -R ubuntu:ubuntu /home/ubuntu/app
su - ubuntu -c 'cd /home/ubuntu/app && npm install --no-audit --no-fund --loglevel=error'
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

	client := slicer.NewSlicerClient(baseURL, token, "slicer-bgexec-npm-dev/1.0", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	log.Printf("=== npm run dev: background exec with detach + reattach via from_id ===")
	tag := fmt.Sprintf("bgexec=npm-%s", shortID())
	node, err := client.CreateVMWithOptions(ctx, hostGroup, slicer.SlicerCreateNodeRequest{
		CPUs:     2,
		RamBytes: slicer.GiB(2),
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

	launchStart := time.Now()
	ex, err := client.ExecBackground(ctx, node.Hostname, slicer.ExecBackgroundRequest{
		Command: "npm",
		Args:    []string{"run", "dev"},
		Cwd:     "/home/ubuntu/app",
		UID:     1000,
		GID:     1000,
	})
	if err != nil {
		log.Fatalf("ExecBackground: %v", err)
	}
	log.Printf("launch exec_id=%s pid=%d ring=%d elapsed=%s",
		ex.ExecID, ex.PID, ex.RingBytes, time.Since(launchStart))

	// Phase A: follow until "Ready"/"Local:" marker, then disconnect.
	followCtx, followCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer followCancel()

	var lastSeenID uint64
	sawReady := false
	readinessLogs, err := client.ExecLogs(followCtx, node.Hostname, ex.ExecID,
		slicer.LogOptions{Follow: true, FromID: 0})
	if err != nil {
		log.Fatalf("ExecLogs phase A: %v", err)
	}
	log.Printf("--- dev-server output (phase A) ---")
	for f := range readinessLogs {
		if f.ID > lastSeenID {
			lastSeenID = f.ID
		}
		if f.Type == "stdout" || f.Type == "stderr" {
			data := frameText(f)
			// Show the caller what's streaming by forwarding to our own stdout.
			_, _ = os.Stdout.WriteString(data)
			if strings.Contains(data, "Ready in") || strings.Contains(data, "Local:") {
				sawReady = true
				log.Printf("ready marker id=%d: %s", f.ID, strings.TrimSpace(firstLine(data)))
				followCancel()
			}
		}
		if f.Type == "exit" {
			log.Fatalf("exited before ready: exit_code=%d", f.ExitCode)
		}
	}
	log.Printf("--- end phase A ---")
	if !sawReady {
		log.Fatal("never saw Next.js ready marker")
	}

	// Phase B: port-forward + HTTP check.
	log.Printf("opening port-forward 127.0.0.1:3000 -> VM:3000")
	fwd, err := forward.Start(ctx, forward.Options{
		BaseURL: baseURL,
		Token:   token,
		VMName:  node.Hostname,
		Specs:   []string{"127.0.0.1:3000:127.0.0.1:3000"},
	})
	if err != nil {
		log.Fatalf("forward.Start: %v", err)
	}
	defer fwd.Close()

	body, err := httpGetRetry(ctx, "http://127.0.0.1:3000/", 10, time.Second)
	if err != nil {
		log.Fatalf("GET through forward: %v", err)
	}
	if !strings.Contains(body, "slicer bg-exec demo") {
		log.Fatalf("unexpected body (%d bytes)", len(body))
	}
	log.Printf("http check passed: %d bytes", len(body))

	// Phase C: disconnect then reattach.
	log.Printf("disconnect 5s then reattach from_id=%d", lastSeenID+1)
	time.Sleep(5 * time.Second)

	reCtx, reCancel := context.WithTimeout(ctx, 30*time.Second)
	defer reCancel()
	resumedLogs, err := client.ExecLogs(reCtx, node.Hostname, ex.ExecID,
		slicer.LogOptions{Follow: true, FromID: lastSeenID + 1})
	if err != nil {
		log.Fatalf("ExecLogs phase C: %v", err)
	}
	log.Printf("--- resumed output (phase C, from_id=%d) ---", lastSeenID+1)
	var count int
	deadline := time.After(10 * time.Second)
loop:
	for {
		select {
		case <-deadline:
			break loop
		case f, ok := <-resumedLogs:
			if !ok {
				break loop
			}
			count++
			switch f.Type {
			case "gap":
				log.Printf("gap id=%d dropped_frames=%d dropped_bytes=%d: %s",
					f.ID, f.DroppedFrames, f.DroppedBytes, f.Message)
			case "stdout", "stderr":
				_, _ = os.Stdout.WriteString(frameText(f))
			case "exit":
				log.Printf("exit id=%d code=%d signal=%s", f.ID, f.ExitCode, f.Signal)
			}
		}
	}
	reCancel()
	log.Printf("--- end phase C ---")
	log.Printf("reattach ok frames=%d", count)

	// Phase D: info + kill + wait + reap.
	info, err := client.ExecInfo(ctx, node.Hostname, ex.ExecID)
	if err != nil {
		log.Fatalf("ExecInfo: %v", err)
	}
	log.Printf("info running=%v bytes=%d dropped=%d next_id=%d",
		info.Running, info.BytesWritten, info.BytesDropped, info.NextID)

	killRes, err := client.ExecKill(ctx, node.Hostname, ex.ExecID,
		slicer.KillOptions{Signal: "TERM", GraceMs: 3000})
	if err != nil {
		log.Fatalf("ExecKill: %v", err)
	}
	log.Printf("kill signal_sent=%s running=%v", killRes.SignalSent, killRes.Running)

	waitRes, err := client.ExecWaitExit(ctx, node.Hostname, ex.ExecID, 15*time.Second)
	if err != nil {
		log.Fatalf("ExecWaitExit: %v", err)
	}
	log.Printf("wait_exit running=%v exit_code=%d signal=%s timed_out=%v",
		waitRes.Running, deref(waitRes.ExitCode), waitRes.Signal, waitRes.TimedOut)

	if _, err := client.ExecDelete(ctx, node.Hostname, ex.ExecID); err != nil {
		log.Fatalf("ExecDelete: %v", err)
	}
	if _, err := client.ExecInfo(ctx, node.Hostname, ex.ExecID); err == nil {
		log.Fatal("ExecInfo after delete should have failed with 410")
	}
	log.Printf("PASS")
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

func deref(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func httpGetRetry(ctx context.Context, url string, attempts int, delay time.Duration) (string, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			b, rerr := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if rerr == nil && res.StatusCode == 200 {
				return string(b), nil
			}
			lastErr = fmt.Errorf("status=%d err=%v", res.StatusCode, rerr)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	return "", errors.New("retries exhausted: " + lastErr.Error())
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

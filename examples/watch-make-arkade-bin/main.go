// watch-make-arkade-bin demonstrates the Slicer fs watch API by streaming
// filesystem events from a microVM while `make dist` produces arkade
// cross-compiled binaries.
//
// Expected setup inside the VM:
//
//	git clone https://github.com/alexellis/arkade ~/src/arkade
//	arkade system install go
//
// Usage:
//
//	SLICER_URL=https://slicer.example.com SLICER_TOKEN=... \
//	  go run . -path /home/ubuntu/src/arkade/bin
//
// In another terminal (or from your own orchestration), run `make dist`:
//
//	slicer vm exec demo-1 --uid 1000 -- \
//	  "cd ~/src/arkade && PATH=/usr/local/go/bin:$PATH make dist"
//
// Each produced binary prints a single line to stdout as it lands on disk.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	slicer "github.com/slicervm/sdk"
)

func main() {
	var (
		apiURL  = flag.String("url", os.Getenv("SLICER_URL"), "Slicer API URL (overrides SLICER_URL)")
		token   = flag.String("token", os.Getenv("SLICER_TOKEN"), "Bearer token (overrides SLICER_TOKEN)")
		watch   = flag.String("path", "/home/ubuntu/src/arkade/bin", "Directory inside the VM to watch")
		pattern = flag.String("pattern", "arkade*", "Glob patterns, comma-separated")
		uid     = flag.Uint("uid", 1000, "UID on the guest used to resolve ~ in paths")
		verbose = flag.Bool("v", false, "Print every event, including Go toolchain tmp write-and-rename churn")
		timeout = flag.Duration("timeout", 10*time.Minute, "Stop watching after this duration")
	)
	flag.Parse()

	if *apiURL == "" {
		log.Printf("url not given")
		flag.Usage()
		os.Exit(2)
	}

	var (
		client *slicer.SlicerClient
		err    error
	)
	if *token != "" {
		client = slicer.NewSlicerClient(*apiURL, *token, "slicer-watch-example/1.0", nil)
	} else {
		client, err = slicer.NewClientFromEnv(*apiURL, "slicer-watch-example/1.0", nil)
		if err != nil {
			log.Fatalf("client: %v", err)
		}
	}

	hg, err := client.GetHostGroups(context.TODO())
	if err != nil {
		log.Fatalf("get host groups: %v", err)
	}

	uuid := uuid.New()

	vm, err := client.CreateVM(context.Background(), hg[0].Name, slicer.SlicerCreateNodeRequest{
		Tags: []string{"uuid=" + uuid.String(), "demo=make-arkade-bin"},
	})
	if err != nil {
		log.Fatalf("create vm: %v", err)
	}

	fmt.Fprintf(os.Stderr, "created vm %s\n", vm.Hostname)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	patterns := splitCSV(*pattern)

	req := slicer.SlicerFSWatchRequest{
		Paths:     []string{*watch},
		Patterns:  patterns,
		Recursive: true,
		UID:       uint32(*uid),
	}

	fmt.Fprintf(os.Stderr, "watching %s:%s (patterns=%v, timeout=%s)\n", vm.Hostname, *watch, patterns, *timeout)

	binaries := map[string]int64{}
	for evt, err := range client.WatchFSIter(ctx, vm.Hostname, req) {
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Fatalf("stream: %v", err)
		}

		// Go toolchain writes each output via <name>-go-tmp-umask + rename.
		// Suppress that noise unless -v is set; the interesting event is the
		// create on the final name.
		if !*verbose && strings.Contains(evt.Path, "-go-tmp-umask") {
			continue
		}

		switch evt.Type {
		case "create":
			name := filepath.Base(evt.Path)
			if prev, ok := binaries[evt.Path]; !ok || prev != evt.Size {
				binaries[evt.Path] = evt.Size
				fmt.Printf("[%d] %-28s %10d bytes  %s\n", evt.ID, name, evt.Size, evt.Timestamp)
			}
		case "write", "chmod", "remove", "rename":
			if *verbose {
				fmt.Printf("[%d] %-8s %s (%d)\n", evt.ID, evt.Type, evt.Path, evt.Size)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "\nfinal: %d binaries observed\n", len(binaries))
	for path, size := range binaries {
		fmt.Fprintf(os.Stderr, "  %s (%d bytes)\n", path, size)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// shell-test serves a minimal React + xterm.js UI that connects to a Slicer
// VM shell via the SDK's shell.ProxyHandler.
//
// No auth, no users — purely for integration testing.
//
// Usage:
//
//	cd web && npm run build && cd ..
//	go run . --url /tmp/shell-test/shell-test.sock
//	# open http://<host>:3333
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	slicer "github.com/slicervm/sdk"
	"github.com/slicervm/sdk/shell"
)

//go:embed web/dist/*
var webFiles embed.FS

func main() {
	var (
		slicerURL = flag.String("url", "", "Slicer API URL or unix socket path (required)")
		token     = flag.String("token", "", "Bearer token for Slicer API (optional)")
		listen    = flag.String("listen", "0.0.0.0:3333", "Listen address")
		shellBin  = flag.String("shell", "/bin/bash", "Shell binary inside the VM")
	)
	flag.Parse()

	if *slicerURL == "" {
		flag.Usage()
		log.Fatal("--url is required")
	}

	logger := log.New(log.Writer(), "[shell-test] ", log.LstdFlags)
	client := slicer.NewSlicerClient(*slicerURL, *token, "shell-test/1.0", nil)

	shellHandler := shell.NewProxyHandler(shell.Options{
		BaseURL: *slicerURL,
		Token:   *token,
		Shell:   *shellBin,
		VMNameFunc: func(r *http.Request) string {
			return r.URL.Query().Get("vm")
		},
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		Logger: logger,
	})

	distFS, err := fs.Sub(webFiles, "web/dist")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/ws/shell", shellHandler)

	mux.HandleFunc("/api/vms", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		vms, err := client.ListVMs(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vms)
	})

	mux.HandleFunc("/api/hostgroups", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		groups, err := client.GetHostGroups(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(groups)
	})

	mux.HandleFunc("/api/launch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		group := r.URL.Query().Get("group")
		if group == "" {
			http.Error(w, "group query param required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		vm, err := client.CreateVMWithOptions(ctx, group, slicer.SlicerCreateNodeRequest{},
			slicer.SlicerCreateNodeOptions{Wait: slicer.SlicerCreateNodeWaitAgent, Timeout: 2 * time.Minute})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vm)
	})

	mux.Handle("/", http.FileServer(http.FS(distFS)))

	fmt.Printf("Shell test UI: http://%s\n", *listen)
	fmt.Printf("Slicer API: %s\n", *slicerURL)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

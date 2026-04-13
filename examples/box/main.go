package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	slicer "github.com/slicervm/sdk"
)

func main() {
	baseURL := os.Getenv("SLICER_URL")
	if baseURL == "" {
		baseURL = "https://box.slicervm.com"
	}
	log.Printf("configured base_url=%s", baseURL)

	client, err := slicer.NewClientFromEnv(baseURL, "box-client/1.0", http.DefaultClient)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("listing VMs")
	vms, err := client.ListVMs(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listed VMs count=%d", len(vms))
	if len(vms) == 0 {
		log.Fatal("no VMs available")
	}

	log.Printf("running command hostname=%s command=%q", vms[0].Hostname, "uname -a")
	out, err := client.
		CommandContext(ctx, vms[0].Hostname, "uname", "-a").
		CombinedOutput()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(string(out))
}

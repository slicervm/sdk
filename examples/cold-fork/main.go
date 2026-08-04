package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	slicer "github.com/slicervm/sdk"
)

func main() {
	var apiURL, token, source, cacheKey string
	flag.StringVar(&apiURL, "url", os.Getenv("SLICER_URL"), "Slicer API URL or socket")
	flag.StringVar(&token, "token", os.Getenv("SLICER_TOKEN"), "Slicer API bearer token")
	flag.StringVar(&source, "source", "", "stopped persistent source VM")
	flag.StringVar(&cacheKey, "cache-key", "sdk-cold-fork-example", "commit cache key")
	flag.Parse()
	if apiURL == "" || source == "" {
		log.Fatal("--url and --source are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := slicer.NewSlicerClient(apiURL, token, "slicer-sdk-cold-fork-example", nil)

	commit, err := client.CommitVMWithOptions(ctx, source, slicer.SlicerCommitVMOptions{CacheKey: cacheKey})
	if err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("commit=%s\n", commit.CommitID)

	commits, err := client.ListCommits(ctx, slicer.SlicerCommitListOptions{CacheKey: cacheKey})
	if err != nil || len(commits) != 1 || commits[0].CommitID != commit.CommitID {
		log.Fatalf("list commit: commits=%#v err=%v", commits, err)
	}

	emptyAllow := []string{}
	fork, err := commit.Fork(ctx,
		slicer.WithTimeout(2*time.Minute),
		slicer.WithNetwork(&slicer.SlicerForkVMNetworkPolicy{Allow: &emptyAllow}),
		slicer.WithTags("example=cold-fork"),
	)
	if err != nil {
		log.Fatalf("fork: %v", err)
	}
	fmt.Printf("child=%s\n", fork.Hostname)

	description, err := client.DescribeVM(ctx, fork.Hostname)
	if err != nil {
		log.Fatalf("describe: %v", err)
	}
	if description.ParentCommitID != commit.CommitID || description.Network.Override == nil || description.Network.Override.Allow == nil || len(description.Network.Override.Allow) != 0 {
		log.Fatalf("unexpected child description: %#v", description)
	}

	if _, err := client.DeleteVM(ctx, description.HostGroup, fork.Hostname); err != nil {
		log.Fatalf("delete child: %v", err)
	}
	if _, err := client.DeleteCommit(ctx, commit.CommitID); err == nil {
		log.Fatal("commit deletion unexpectedly succeeded while the source VM exists")
	}
	if _, err := client.DeleteVM(ctx, description.HostGroup, source); err != nil {
		log.Fatalf("delete source: %v", err)
	}
	if _, err := client.DeleteCommit(ctx, commit.CommitID); err != nil {
		log.Fatalf("delete commit: %v", err)
	}
	fmt.Println("cleanup=complete")
}

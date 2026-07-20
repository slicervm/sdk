package slicer

import (
	"strings"
	"testing"
)

func TestCopyManifestValidate(t *testing.T) {
	digest := strings.Repeat("a", 64)
	manifest := CopyManifest{
		Version:     ChunkedCopyManifestVersion,
		Mode:        "binary",
		Destination: "/home/ubuntu/tool",
		Size:        3,
		SHA256:      digest,
		Chunks: []CopyChunk{{
			Index:  0,
			Size:   3,
			SHA256: digest,
		}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}

	badIndex := manifest
	badIndex.Chunks = append([]CopyChunk(nil), manifest.Chunks...)
	badIndex.Chunks[0].Index = 1
	if err := badIndex.Validate(); err == nil {
		t.Fatal("out-of-sequence chunk was accepted")
	}

	badSize := manifest
	badSize.Size++
	if err := badSize.Validate(); err == nil {
		t.Fatal("incorrect stream size was accepted")
	}
}

func TestCopyChunkFileName(t *testing.T) {
	chunk := CopyChunk{Index: 12, SHA256: strings.Repeat("b", 64)}
	want := "000012-" + strings.Repeat("b", 64) + ".chunk"
	if got := CopyChunkFileName(chunk); got != want {
		t.Fatalf("CopyChunkFileName() = %q, want %q", got, want)
	}
}
